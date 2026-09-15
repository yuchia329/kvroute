package bench

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/yuchia329/kvroute/internal/record"
)

// The spill rule's two branches are only two branches if they read two
// signals, and this is where that is checked rather than asserted.
//
// #16 built the rule on the premise that a KV high-water mark answered memory
// pressure while an inflight multiple answered load imbalance, and #18's
// headline figure rests on the same premise. It was false: the gauge the first
// branch read counts the blocks held by the running batch, so it tracked the
// second branch's own signal at r = 0.973 and the grid was sweeping one
// pressure twice (ADR-0011). Nothing in the code said so, and no cell could
// have.
//
// So separability is now a measurement a run produces, on the same footing as
// its goodput: every router row carries the residency signal, the batch gauge
// and the router's own inflight, taken at the same instant, and SpillSignals
// reports each signal's observed range and how strongly it moves with inflight.
// A run whose residency signal correlates with inflight the way the old gauge
// did has not fixed anything, and this is what says so.

// SpillSignals is what one set of router rows says about the two signals the
// spill rule's branches read.
type SpillSignals struct {
	// Rows is how many decisions the report covers.
	Rows int `json:"rows"`
	// HitRate is the residency signal the spill rule reads: the range it was
	// observed over, and how strongly it moved with inflight. This is the pair
	// #28 is answered by — the range says which low-water levels are reachable,
	// and the correlation says whether the branch that reads it is a second
	// branch at all.
	HitRate       Range       `json:"hit_rate"`
	HitRateVsLoad Correlation `json:"hit_rate_vs_inflight"`
	// Honoured is the candidate the hit rate replaced, reported beside it rather
	// than dropped. #28's first run measured it saturating — 70% of readings
	// exactly 1.0, and no window recovered a range — and a run that shows that
	// again in one line is cheaper than a run that has to rediscover it.
	Honoured Range       `json:"honoured"`
	VsLoad   Correlation `json:"honoured_vs_inflight"`
	// BatchKV is the gauge the branch used to read, kept for the comparison and
	// for nothing else. It is the control: its correlation with inflight is the
	// 0.973 the new signal has to come in materially below, measured on the same
	// rows rather than quoted from the ticket that found it.
	BatchKV      Range       `json:"batch_kv"`
	BatchKVVLoad Correlation `json:"batch_kv_vs_inflight"`
}

// Correlation is a Pearson correlation with the sample it was taken over.
//
// The count travels with the coefficient because an r taken over forty rows and
// one taken over forty thousand are different claims, and this figure is the
// one the whole separability argument turns on.
//
// Defined is false when there is nothing to correlate: fewer than two paired
// readings, or a signal that never varied. A constant signal has no correlation
// with anything, and reporting that as 0 would read as perfect independence —
// which is precisely the answer this report exists to be sceptical of.
type Correlation struct {
	R       float64 `json:"r"`
	Pairs   int     `json:"pairs"`
	Defined bool    `json:"defined"`
}

func (c Correlation) String() string {
	if !c.Defined {
		return "undefined (nothing varied)"
	}
	return fmt.Sprintf("r = %.3f over %d pairs", c.R, c.Pairs)
}

// Range is what a signal was observed to do over a run.
type Range struct {
	// Readings is how many rows carried one, and Unread how many did not. The
	// pair is the first thing to read: a run whose rows were mostly unread
	// measured a condition that was mostly disabled, whatever the quantiles say.
	Readings int     `json:"readings"`
	Unread   int     `json:"unread"`
	Min      float64 `json:"min"`
	P10      float64 `json:"p10"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	Max      float64 `json:"max"`
}

// Reaches reports whether a low-water mark would ever have fired over the run
// this range came from: strictly, whether anything was observed below it.
//
// This is #28's acceptance criterion turned into a function, so a sweep can
// refuse a grid point the signal never reached instead of spending a run
// discovering it. A level at or below the minimum observed is not a
// conservative setting, it is a cell that cannot produce a decision — which is
// what two of #16's three levels were.
func (r Range) Reaches(lowWater float64) bool { return r.Readings > 0 && r.Min < lowWater }

// String renders the range as the line a run's report carries.
func (r Range) String() string {
	if r.Readings == 0 {
		return "no rows carried a reading"
	}
	return fmt.Sprintf("%d read, %d unread: min %.3f, p10 %.3f, p50 %.3f, p90 %.3f, max %.3f",
		r.Readings, r.Unread, r.Min, r.P10, r.P50, r.P90, r.Max)
}

// reading is one row's value of a signal, and whether the row had one.
type reading func(record.Request) (float64, bool)

func hitRateReading(r record.Request) (float64, bool)  { return r.HitRate, r.HitRateRead }
func honouredReading(r record.Request) (float64, bool) { return r.HonouredRate, r.HonouredRead }
func batchKVReading(r record.Request) (float64, bool)  { return r.BatchKVOccupancy, r.BatchKVRead }

// MeasureSpillSignals reads both signals off a set of router rows.
func MeasureSpillSignals(rows []record.Request) SpillSignals {
	return SpillSignals{
		Rows:          len(rows),
		HitRate:       RangeOf(rows, hitRateReading),
		HitRateVsLoad: CorrelateWithInflight(rows, hitRateReading),
		Honoured:      RangeOf(rows, honouredReading),
		VsLoad:        CorrelateWithInflight(rows, honouredReading),
		BatchKV:       RangeOf(rows, batchKVReading),
		BatchKVVLoad:  CorrelateWithInflight(rows, batchKVReading),
	}
}

// RangeOf is the distribution of one signal over a set of rows.
//
// Only the rows that carried a reading count towards the quantiles. A row whose
// replica had no reading is counted as unread rather than folded in as a zero:
// a signal reported at the bottom of its range because nobody measured it is
// the exact shape a grid would then be cut to chase.
//
// Quantiles rather than a mean, because a spill threshold is a threshold. What
// a low-water level has to be reachable against is the bottom of the
// distribution, and a mean says nothing about where the bottom is.
func RangeOf(rows []record.Request, read reading) Range {
	values := make([]float64, 0, len(rows))
	for _, row := range rows {
		if v, ok := read(row); ok {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return Range{Unread: len(rows)}
	}
	slices.Sort(values)
	return Range{
		Readings: len(values),
		Unread:   len(rows) - len(values),
		Min:      values[0],
		P10:      quantileOf(values, 0.10),
		P50:      quantileOf(values, 0.50),
		P90:      quantileOf(values, 0.90),
		Max:      values[len(values)-1],
	}
}

// CorrelateWithInflight is the Pearson correlation between one signal and the
// inflight count recorded on the same row.
//
// The same row on purpose. Both figures were taken from the same snapshot at
// the moment of one decision, so this compares two readings of one instant
// rather than two time series that have to be aligned — which is what let #28
// fit a line through the old gauge at all.
//
// Rows with no reading are dropped rather than paired against a zero. They are
// not evidence of independence and they are not evidence of agreement; they are
// rows where one of the two numbers does not exist.
func CorrelateWithInflight(rows []record.Request, read reading) Correlation {
	var xs, ys []float64
	for _, row := range rows {
		v, ok := read(row)
		if !ok {
			continue
		}
		xs = append(xs, float64(row.Inflight))
		ys = append(ys, v)
	}
	return pearson(xs, ys)
}

// pearson is the correlation coefficient of two equal-length samples.
//
// Computed from the centred sums rather than from the raw ones, which is the
// numerically stable form: the raw form differences two large nearly-equal
// quantities and can return an |r| above 1 on samples this size.
func pearson(xs, ys []float64) Correlation {
	if len(xs) < 2 || len(xs) != len(ys) {
		return Correlation{}
	}
	var meanX, meanY float64
	for i := range xs {
		meanX += xs[i]
		meanY += ys[i]
	}
	meanX /= float64(len(xs))
	meanY /= float64(len(ys))

	var sxy, sxx, syy float64
	for i := range xs {
		dx, dy := xs[i]-meanX, ys[i]-meanY
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		// One of them never moved. There is no correlation to report, and a 0
		// here would read as independence rather than as absence.
		return Correlation{Pairs: len(xs)}
	}
	return Correlation{R: sxy / math.Sqrt(sxx*syy), Pairs: len(xs), Defined: true}
}

// quantileOf is the q-th quantile of a sorted sample by nearest rank, which is
// a reading that was actually taken rather than an interpolation between two
// that were.
func quantileOf(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)))
	return sorted[min(i, len(sorted)-1)]
}

// Report renders what the rows said, in the form #28's acceptance criteria are
// checked against.
//
// It states the comparison rather than leaving it to the reader: the old
// gauge's correlation with inflight is printed beside the new signal's, on the
// same rows, because "materially below 0.973" is a claim about two numbers and
// only one of them is new.
func (s SpillSignals) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# The spill rule's two signals\n\n")
	fmt.Fprintf(&b, "%d routing decisions.\n\n", s.Rows)

	fmt.Fprintf(&b, "| signal | observed range | correlation with inflight |\n")
	fmt.Fprintf(&b, "|---|---|---|\n")
	fmt.Fprintf(&b, "| prefix cache hit rate (residency branch) | %s | %s |\n", s.HitRate, s.HitRateVsLoad)
	fmt.Fprintf(&b, "| honoured rate (measured, not routed on) | %s | %s |\n", s.Honoured, s.VsLoad)
	fmt.Fprintf(&b, "| batch KV occupancy (not routed on) | %s | %s |\n", s.BatchKV, s.BatchKVVLoad)
	fmt.Fprintf(&b, "\n")

	switch {
	case !s.HitRateVsLoad.Defined:
		fmt.Fprintf(&b, "The residency signal did not vary over these rows, so nothing can be said about\n"+
			"whether it is separable from load. A run whose replicas never evicted is not a\n"+
			"run this grid can be cut from.\n\n")
	default:
		fmt.Fprintf(&b, "The residency signal moves with inflight at r = %.3f. The gauge it replaced moved\n"+
			"with inflight at r = 0.973 over #16's rows", s.HitRateVsLoad.R)
		if s.BatchKVVLoad.Defined {
			fmt.Fprintf(&b, ", and at r = %.3f over these", s.BatchKVVLoad.R)
		}
		fmt.Fprintf(&b, ".\n\n")
	}

	if s.HitRate.Readings == 0 {
		fmt.Fprintf(&b, "No row carried a residency reading, so the branch that reads it was disabled for\n"+
			"the whole run however it was configured. Check that the router was started with\n"+
			"-scrape-replicas, and that the engines publish the prefix-cache counters.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Levels reachable at this rung are those above %.3f, the lowest rate observed.\n"+
		"A level at or below it cannot fire, which is what two of #16's three were.\n",
		s.HitRate.Min)
	return b.String()
}
