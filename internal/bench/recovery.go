package bench

import (
	"math"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
)

// CurvePoint is one bucket of a recovery curve: the requests offered in one
// slice of time around a fault, and what became of them.
//
// Bucketed by when a request was offered rather than when it finished, so a
// bucket's goodput is read against the load it was given. A request offered the
// instant before a replica died belongs to the fleet that was whole, however
// long it then took to be answered; bucketing by completion would smear every
// slow request into the buckets after it and move the dip.
type CurvePoint struct {
	// AtNs is where the bucket starts, relative to the fault: negative before it.
	AtNs int64 `json:"at_ns"`

	Offered       int `json:"offered"`
	Successes     int `json:"successes"`
	SLOViolations int `json:"slo_violations"`
	// Rerouted counts the requests moved off a failed replica before their first
	// token. Most of them are also successes; this is the loss they hid.
	Rerouted  int `json:"rerouted"`
	Dropped   int `json:"dropped"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`

	// GoodputRPS is the requests offered in this bucket that met the SLO, per
	// second of the bucket. Zero when no SLO was applied, as in the summary: a
	// goodput nobody checked against anything is not a goodput.
	GoodputRPS float64 `json:"goodput_rps"`
}

// RecoveryCurve buckets a run's measured requests around a fault.
//
// Every bucket from the first measured request to the last is present,
// including ones in which nothing was offered: a gap in the offered load is a
// fact about the run, and closing over it would draw a line through a stretch
// the driver never measured. Warm-up requests are excluded, as they are from
// every other figure.
//
// The buckets are anchored at the fault, so the fault falls exactly on a bucket
// boundary and never splits one between a whole fleet and a broken one.
//
// end is where the run stopped offering load. The driver keeps its own clock,
// started a moment after the run's, so the last arrival can be sent a hair past
// end; it counts in the bucket end closes rather than opening one of its own,
// where one request across a whole bucket would read as goodput collapsing as
// the run finished.
func RecoveryCurve(rows []Result, fault, end time.Time, bucket time.Duration, slo SLO) []CurvePoint {
	if bucket <= 0 {
		return nil
	}
	width := bucket.Nanoseconds()
	closing := floorDiv(end.UnixNano()-fault.UnixNano()-1, width)
	points := map[int64]*CurvePoint{}
	met := map[int64]int{}
	first, last := int64(math.MaxInt64), int64(math.MinInt64)

	for _, r := range rows {
		if r.Warmup {
			continue
		}
		i := min(floorDiv(r.StartedAtNs-fault.UnixNano(), width), closing)
		first, last = min(first, i), max(last, i)
		p := points[i]
		if p == nil {
			p = &CurvePoint{AtNs: i * width}
			points[i] = p
		}
		p.Offered++
		if r.Reroutes > 0 {
			p.Rerouted++
		}
		switch r.Outcome {
		case record.OutcomeSuccess:
			p.Successes++
			// The summary's rule exactly: violations and goodput are counted only
			// against an SLO that was applied.
			if slo.Applied() {
				if slo.Met(r) {
					met[i]++
				} else {
					p.SLOViolations++
				}
			}
		case record.OutcomeDropped:
			p.Dropped++
		case record.OutcomeFailed:
			p.Failed++
		case record.OutcomeCancelled:
			p.Cancelled++
		}
	}
	if len(points) == 0 {
		return nil
	}

	curve := make([]CurvePoint, 0, last-first+1)
	for i := first; i <= last; i++ {
		p := CurvePoint{AtNs: i * width}
		if held := points[i]; held != nil {
			p = *held
		}
		p.GoodputRPS = float64(met[i]) / bucket.Seconds()
		curve = append(curve, p)
	}
	return curve
}

// floorDiv divides rounding towards negative infinity, so that a request
// offered a moment before the fault lands in the bucket before it rather than in
// the fault's own.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Recovery is what a recovery curve says in numbers.
//
// "The fault on" starts one bucket before the fault. Requests offered in the
// bucket the fault came at the end of were still in flight when the replica
// went, and a kill drops the ones that were streaming; by the time they were
// offered they land in that bucket, so it is the fault's rather than the
// baseline's. One bucket is enough because a chaos run is driven below the
// fleet's knee, where a request is answered well inside one.
type Recovery struct {
	// BaselineRPS is the mean goodput over the buckets before the one the fault
	// came at the end of: what the whole fleet delivered at this load.
	BaselineRPS float64 `json:"baseline_rps"`
	// TroughRPS is the lowest goodput of any bucket from the fault on, and
	// TroughAtNs where it fell.
	TroughRPS  float64 `json:"trough_rps"`
	TroughAtNs int64   `json:"trough_at_ns"`
	// DeficitRequests is how many requests that would have met the SLO at the
	// baseline did not, summed over every bucket from the fault on to the end of
	// the run: the area between the baseline and the curve.
	//
	// It is the one figure that does not depend on choosing a tolerance, and the
	// one that counts both of the dips the two affinity policies are predicted to
	// differ by — when the replica leaves, and when it returns.
	DeficitRequests float64 `json:"deficit_requests"`
	// Steady is whether goodput came back to within Tolerance of the baseline and
	// stayed there to the end of the run, and SteadyAtNs is the start of the
	// first bucket from which it did.
	Steady     bool    `json:"steady"`
	SteadyAtNs int64   `json:"steady_at_ns"`
	Tolerance  float64 `json:"tolerance"`
}

// MeasureRecovery reads a curve's baseline, trough, deficit and recovery.
//
// Steady is measured back from the end of the run rather than forward from the
// fault. Read forward, the first healthy-looking bucket would stop the clock, and
// a policy that dips a second time when the replica returns — which is what
// session affinity's ring is predicted to do, moving that replica's sessions back
// onto an empty cache — would be reported as having recovered as fast as one that
// dips once. Read back from the end, steady is after the last dip, whichever one
// that was.
//
// A curve with no bucket before the fault's own has no baseline, and one with
// nothing from the fault on has nothing to have recovered from; both report the
// zero value.
func MeasureRecovery(curve []CurvePoint, bucket time.Duration, tolerance float64) Recovery {
	r := Recovery{Tolerance: tolerance}
	faultOn := -bucket.Nanoseconds()
	var before float64
	var baselineBuckets int
	var after []CurvePoint
	for _, p := range curve {
		if p.AtNs < faultOn {
			before += p.GoodputRPS
			baselineBuckets++
		} else {
			after = append(after, p)
		}
	}
	if baselineBuckets == 0 || len(after) == 0 {
		return r
	}
	r.BaselineRPS = before / float64(baselineBuckets)

	r.TroughRPS, r.TroughAtNs = after[0].GoodputRPS, after[0].AtNs
	for _, p := range after {
		if p.GoodputRPS < r.TroughRPS {
			r.TroughRPS, r.TroughAtNs = p.GoodputRPS, p.AtNs
		}
		if short := r.BaselineRPS - p.GoodputRPS; short > 0 {
			r.DeficitRequests += short * bucket.Seconds()
		}
	}

	floor := (1 - tolerance) * r.BaselineRPS
	for i := len(after) - 1; i >= 0 && after[i].GoodputRPS >= floor; i-- {
		r.Steady, r.SteadyAtNs = true, after[i].AtNs
	}
	return r
}
