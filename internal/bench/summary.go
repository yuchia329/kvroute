package bench

import (
	"fmt"
	"slices"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/stats"
)

// DefaultFailureThreshold is the failure rate above which a cell is flagged.
// §6 puts it at ~1%: past that the cell is not describing a healthy fleet and
// averaging it in with cells that are would hide the fact.
const DefaultFailureThreshold = 0.01

// DefaultWarmupDriftThreshold is how much slower a cell's first measured half
// may be than its second before the cell is flagged as still warming up.
//
// Generous on purpose. The point of the check is to catch a warm-up that was
// plainly too short, not to police ordinary run-to-run wobble: a threshold that
// fires on noise would flag every cell, and a flag that fires on every cell
// stops being read.
const DefaultWarmupDriftThreshold = 0.25

// DefaultScheduleLagThreshold is how far behind its own arrival schedule an
// open-loop cell may fall, at the 99th percentile, before it is flagged.
//
// The fleet's own latencies are hundreds of milliseconds, so a driver that is
// more than a tenth of a second late on one request in a hundred is
// contributing meaningfully to the numbers it reports — and, worse, is
// throttling itself in exactly the way the open-loop driver exists to avoid.
// Below that the lateness is host scheduling noise on top of load the fleet did
// receive.
const DefaultScheduleLagThreshold = 100 * time.Millisecond

// SummaryOptions are the thresholds a cell is judged against. They travel
// together everywhere, so they are one type rather than three arguments.
type SummaryOptions struct {
	SLO SLO
	// FailureThreshold is the dropped-plus-failed rate above which a cell is
	// flagged. Zero uses DefaultFailureThreshold.
	FailureThreshold float64
	// WarmupDriftThreshold is the first-half-to-second-half slowdown above
	// which a cell is flagged as under-warmed. Zero uses
	// DefaultWarmupDriftThreshold; a negative value disables the check.
	WarmupDriftThreshold float64
	// ScheduleLagThreshold is how late an open-loop cell's requests may be sent
	// against the schedule that asked for them, at the 99th percentile, before
	// the cell is flagged. Zero uses DefaultScheduleLagThreshold; a negative
	// value disables the check. It says nothing about a closed-loop cell, which
	// has no schedule to be late against.
	ScheduleLagThreshold time.Duration
}

func (o SummaryOptions) failureThreshold() float64 {
	if o.FailureThreshold == 0 {
		return DefaultFailureThreshold
	}
	return o.FailureThreshold
}

func (o SummaryOptions) driftThreshold() float64 {
	if o.WarmupDriftThreshold == 0 {
		return DefaultWarmupDriftThreshold
	}
	return o.WarmupDriftThreshold
}

func (o SummaryOptions) scheduleLagThreshold() time.Duration {
	if o.ScheduleLagThreshold == 0 {
		return DefaultScheduleLagThreshold
	}
	return o.ScheduleLagThreshold
}

// Summary is the arithmetic over one cell's rows.
//
// Dropped, Failed and SLOViolations are three columns and stay three columns.
// Every percentile here is over successful responses only.
type Summary struct {
	Requests      int `json:"requests" parquet:"requests"`
	Warmup        int `json:"warmup" parquet:"warmup"`
	Successes     int `json:"successes" parquet:"successes"`
	Failed        int `json:"failed" parquet:"failed"`
	Dropped       int `json:"dropped" parquet:"dropped"`
	Cancelled     int `json:"cancelled" parquet:"cancelled"`
	SLOViolations int `json:"slo_violations" parquet:"slo_violations"`

	// WindowNs is the span the rates below are computed over, derived from the
	// rows rather than from the driver's clock so that the denominator cannot
	// disagree with the rows it divides.
	//
	// It is the window load was *offered* in, which is not the same span under
	// the two drivers. A closed-loop cell offers load for as long as it is
	// receiving responses, so the window is its first measured request's start
	// to the last one's finish. An open-loop cell offers load on a schedule and
	// then waits for what is still in flight, and past saturation that drain
	// tail runs well past the last arrival — counting it would divide the
	// requests offered in a minute by a minute and a half and call the result a
	// rate. So a scheduled cell's window is its schedule: first arrival due to
	// last arrival due, plus the one inter-arrival gap the last arrival owns.
	//
	// Requests that finished after the window still count in the numerator.
	// They were offered inside it, and dropping them would let a fleet improve
	// its goodput by being too slow to answer before the cell ended.
	WindowNs int64 `json:"window_ns" parquet:"window_ns"`
	// ThroughputRPS counts every response that completed, however slow.
	// GoodputRPS counts only those that completed inside the SLO, and is the
	// primary metric. A dropped or failed request produced no response and is
	// in neither.
	ThroughputRPS float64 `json:"throughput_rps" parquet:"throughput_rps"`
	GoodputRPS    float64 `json:"goodput_rps" parquet:"goodput_rps"`

	TTFTP50Ns  int64 `json:"ttft_p50_ns" parquet:"ttft_p50_ns"`
	TTFTP95Ns  int64 `json:"ttft_p95_ns" parquet:"ttft_p95_ns"`
	TTFTP99Ns  int64 `json:"ttft_p99_ns" parquet:"ttft_p99_ns"`
	TotalP50Ns int64 `json:"total_p50_ns" parquet:"total_p50_ns"`
	TotalP99Ns int64 `json:"total_p99_ns" parquet:"total_p99_ns"`
	// ITL percentiles are taken across the per-request median inter-token
	// latencies, not across every pooled gap: the rows carry each request's ITL
	// summary rather than its individual gaps.
	ITLP50Ns int64 `json:"itl_p50_ns" parquet:"itl_p50_ns"`
	ITLP95Ns int64 `json:"itl_p95_ns" parquet:"itl_p95_ns"`

	// SLOApplied distinguishes "nothing violated" from "nothing was checked".
	// The concurrency-1 cell is what the SLO is derived from, so it necessarily
	// runs before one exists, and a bare zero in the violations column would
	// read as the former.
	SLOApplied bool  `json:"slo_applied" parquet:"slo_applied"`
	SLOTTFTNs  int64 `json:"slo_ttft_ns" parquet:"slo_ttft_ns"`
	SLOITLNs   int64 `json:"slo_itl_ns" parquet:"slo_itl_ns"`

	// WarmupDrift is how much slower the first half of the measured window was
	// than the second, by TTFT p50: 0.30 means the first half was 30% slower.
	//
	// It exists so the warm-up length can be checked rather than trusted. A
	// cell whose measured window is still speeding up is still warming up, and
	// no constant chosen in advance can prove otherwise for every concurrency
	// level. NaN-free: zero when there are too few successes to compare.
	WarmupDrift float64 `json:"warmup_drift" parquet:"warmup_drift"`

	// Scheduled is how many measured requests carried a due time, which is all
	// of them under the open-loop driver and none under the closed-loop one.
	// The lag figures below are over those requests, whatever their outcome:
	// how late the driver was is a property of the driver, not of what came
	// back.
	Scheduled        int   `json:"scheduled" parquet:"scheduled"`
	ScheduleLagP50Ns int64 `json:"schedule_lag_p50_ns" parquet:"schedule_lag_p50_ns"`
	ScheduleLagP99Ns int64 `json:"schedule_lag_p99_ns" parquet:"schedule_lag_p99_ns"`
	ScheduleLagMaxNs int64 `json:"schedule_lag_max_ns" parquet:"schedule_lag_max_ns"`
	// ScheduleLagThresholdNs is the p99 lag above which this cell was flagged
	// as not having held its schedule, so the flag can be read against what it
	// was judged by.
	ScheduleLagThresholdNs int64 `json:"schedule_lag_threshold_ns" parquet:"schedule_lag_threshold_ns"`

	// PromptBytes is the prompt bytes this cell offered over its measured
	// window, counted across every request it sent rather than only the ones
	// that came back.
	//
	// Offered rather than served, because it is the numerator of the prompt
	// bytes-per-token ratio and the engines' prompt-token counters on the other
	// side of that ratio count what they were asked to process. Restricting it
	// to successes would put a fleet's failures into a conversion factor that
	// has nothing to do with them.
	PromptBytes int64 `json:"prompt_bytes" parquet:"prompt_bytes"`

	// Decisions is how this cell's requests were routed, counted by the reason
	// the policy gave. It is a reported result: see DecisionMix.
	Decisions DecisionMix `json:"decisions" parquet:"decisions"`

	// FailureRate is dropped plus failed over every measured request.
	FailureRate      float64 `json:"failure_rate" parquet:"failure_rate"`
	FailureThreshold float64 `json:"failure_threshold" parquet:"failure_threshold"`
	// Flagged marks a cell that must not be averaged in with the others without
	// someone reading FlagReasons first.
	Flagged     bool     `json:"flagged" parquet:"flagged"`
	FlagReasons []string `json:"flag_reasons,omitempty" parquet:"flag_reasons"`
}

// Summarize computes a cell's summary from its rows.
//
// Warm-up rows are excluded from every count and every percentile but are still
// reported, so the record shows what was set aside as well as what was kept.
func Summarize(results []Result, opts SummaryOptions) Summary {
	slo := opts.SLO
	s := Summary{
		SLOApplied:       slo.Applied(),
		SLOTTFTNs:        slo.TTFT.Nanoseconds(),
		SLOITLNs:         slo.ITL.Nanoseconds(),
		FailureThreshold: opts.failureThreshold(),
	}

	var ttft, total, itl, lag []time.Duration
	var first, last time.Time
	var firstDue, lastDue time.Time
	rate := 0.0
	met := 0

	for _, r := range results {
		if r.Warmup {
			s.Warmup++
			continue
		}
		s.Requests++
		s.PromptBytes += r.PromptBytes
		s.Decisions.count(r.Decision)

		started := time.Unix(0, r.StartedAtNs)
		if first.IsZero() || started.Before(first) {
			first = started
		}
		if ended := r.EndedAt(); ended.After(last) {
			last = ended
		}

		// Before the outcome switch: an arrival that was fired late was late
		// whether or not anything came back, and a driver that fell behind
		// while the fleet was dropping requests is exactly the case the figure
		// exists to catch.
		if r.ScheduledAtNs != 0 {
			s.Scheduled++
			lag = append(lag, r.ScheduleLag())
			due := time.Unix(0, r.ScheduledAtNs)
			if firstDue.IsZero() || due.Before(firstDue) {
				firstDue = due
			}
			if due.After(lastDue) {
				lastDue = due
			}
			// Off the rows, not off a caller's parameter: the rate the window is
			// derived from has to be the rate the requests in it were offered at.
			rate = r.ArrivalRate
		}

		switch r.Outcome {
		case record.OutcomeSuccess:
			s.Successes++
			// Percentiles are over successes only. A request that never
			// produced a response has no latency to contribute, and a fast
			// failure has one that would flatter the fleet.
			ttft = append(ttft, time.Duration(r.TTFTNs))
			total = append(total, time.Duration(r.TotalNs))
			itl = append(itl, time.Duration(r.ITLP50Ns))
			if !s.SLOApplied {
				break
			}
			if slo.Met(r) {
				met++
			} else {
				s.SLOViolations++
			}
		case record.OutcomeFailed:
			s.Failed++
		case record.OutcomeDropped:
			s.Dropped++
		case record.OutcomeCancelled:
			s.Cancelled++
		}
	}

	if s.Requests > 0 {
		s.FailureRate = float64(s.Failed+s.Dropped) / float64(s.Requests)
	}
	if window := measuredWindow(first, last, firstDue, lastDue, rate); window > 0 {
		s.WindowNs = window.Nanoseconds()
		s.ThroughputRPS = float64(s.Successes) / window.Seconds()
		s.GoodputRPS = float64(met) / window.Seconds()
	}

	slices.Sort(ttft)
	slices.Sort(total)
	slices.Sort(itl)
	slices.Sort(lag)
	s.ScheduleLagP50Ns = stats.Quantile(lag, 0.50).Nanoseconds()
	s.ScheduleLagP99Ns = stats.Quantile(lag, 0.99).Nanoseconds()
	if len(lag) > 0 {
		s.ScheduleLagMaxNs = lag[len(lag)-1].Nanoseconds()
	}
	s.TTFTP50Ns = stats.Quantile(ttft, 0.50).Nanoseconds()
	s.TTFTP95Ns = stats.Quantile(ttft, 0.95).Nanoseconds()
	s.TTFTP99Ns = stats.Quantile(ttft, 0.99).Nanoseconds()
	s.TotalP50Ns = stats.Quantile(total, 0.50).Nanoseconds()
	s.TotalP99Ns = stats.Quantile(total, 0.99).Nanoseconds()
	s.ITLP50Ns = stats.Quantile(itl, 0.50).Nanoseconds()
	s.ITLP95Ns = stats.Quantile(itl, 0.95).Nanoseconds()

	s.WarmupDrift = warmupDrift(results, first, last)
	s.ScheduleLagThresholdNs = opts.scheduleLagThreshold().Nanoseconds()
	s.flag(opts.driftThreshold(), opts.scheduleLagThreshold())
	return s
}

// measuredWindow is the span the cell's rates are computed over: the schedule a
// scheduled cell offered load on, and otherwise the span its rows cover.
//
// The distinction is the open-loop driver's whole point arriving in the
// arithmetic. Its cell fires for its duration and then waits out whatever is
// still in flight, so at and past saturation the last response lands well after
// the last arrival. Dividing by that would understate the rate exactly where the
// driver exists to stop the rate being understated.
func measuredWindow(first, last, firstDue, lastDue time.Time, rate float64) time.Duration {
	if !firstDue.IsZero() && rate > 0 {
		// Plus one inter-arrival gap: n arrivals at rate r occupy n/r seconds,
		// and the span between the first and the last is one gap short of that.
		// Without it a cell of one arrival would have no window at all.
		return lastDue.Sub(firstDue) + time.Duration(float64(time.Second)/rate)
	}
	if last.IsZero() || !last.After(first) {
		return 0
	}
	return last.Sub(first)
}

// minHalfForDrift is how many successes each half needs before their medians
// are worth comparing. Below this the comparison is noise.
const minHalfForDrift = 5

// warmupDrift compares TTFT p50 over the first half of the measured window
// against the second. A cell that is still speeding up was not warm when the
// measurement started.
//
// TTFT rather than total latency because it is what queueing and cold caches
// move first, and it is not diluted by however many tokens each response
// happened to generate.
func warmupDrift(results []Result, first, last time.Time) float64 {
	if first.IsZero() || !last.After(first) {
		return 0
	}
	midpoint := first.Add(last.Sub(first) / 2)

	var early, late []time.Duration
	for _, r := range results {
		if r.Warmup || r.Outcome != record.OutcomeSuccess {
			continue
		}
		if time.Unix(0, r.StartedAtNs).Before(midpoint) {
			early = append(early, time.Duration(r.TTFTNs))
		} else {
			late = append(late, time.Duration(r.TTFTNs))
		}
	}
	if len(early) < minHalfForDrift || len(late) < minHalfForDrift {
		return 0
	}
	slices.Sort(early)
	slices.Sort(late)
	lateP50 := stats.Quantile(late, 0.50)
	if lateP50 <= 0 {
		return 0
	}
	return float64(stats.Quantile(early, 0.50)-lateP50) / float64(lateP50)
}

// flag records every reason this cell should not be silently averaged in with
// the others.
func (s *Summary) flag(driftThreshold float64, scheduleLagThreshold time.Duration) {
	if s.Requests == 0 {
		s.Flag("cell produced no measured requests")
	}
	if s.FailureThreshold > 0 && s.FailureRate > s.FailureThreshold {
		s.Flag(fmt.Sprintf("failure rate %.2f%% exceeds the %.2f%% threshold (%d dropped, %d failed of %d)",
			s.FailureRate*100, s.FailureThreshold*100, s.Dropped, s.Failed, s.Requests))
	}
	if driftThreshold > 0 && s.WarmupDrift > driftThreshold {
		s.Flag(fmt.Sprintf("still warming up: the first half of the measured window was %.0f%% slower than the second by TTFT p50, over a %.0f%% threshold. Lengthen the warm-up and re-run",
			s.WarmupDrift*100, driftThreshold*100))
	}
	if s.Scheduled > 0 && scheduleLagThreshold > 0 && s.ScheduleLagP99Ns > scheduleLagThreshold.Nanoseconds() {
		// The open-loop driver's whole claim is that offered load is an input.
		// A driver that fell behind its own schedule offered less than the cell
		// says it did, and the goodput computed from it would be a figure for a
		// rate the fleet was never actually given.
		s.Flag(fmt.Sprintf("the driver did not hold its arrival schedule: 1%% of requests were sent more than %v late (worst %v), over a %v threshold. The offered rate is not the rate this cell reports",
			time.Duration(s.ScheduleLagP99Ns).Round(time.Millisecond),
			time.Duration(s.ScheduleLagMaxNs).Round(time.Millisecond),
			scheduleLagThreshold))
	}
	if s.Cancelled > 0 {
		// The driver lets in-flight requests finish, so a cancellation means
		// the cell was interrupted rather than run to its end.
		s.Flag(fmt.Sprintf("%d requests were cancelled: the cell did not run to completion", s.Cancelled))
	}
}

// Flag records a reason this summary must not be silently averaged in with the
// others. It is exported because the reasons do not all come from the rows: the
// GPUs supply some, and the characterization pass supplies others.
func (s *Summary) Flag(reason string) {
	s.Flagged = true
	s.FlagReasons = append(s.FlagReasons, reason)
}
