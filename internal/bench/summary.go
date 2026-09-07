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

	// WindowNs is the first measured request's start to the last one's finish.
	// It is derived from the rows rather than from the driver's clock so that
	// the rate denominator cannot disagree with the rows it divides.
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

	var ttft, total, itl []time.Duration
	var first, last time.Time
	met := 0

	for _, r := range results {
		if r.Warmup {
			s.Warmup++
			continue
		}
		s.Requests++

		started := time.Unix(0, r.StartedAtNs)
		if first.IsZero() || started.Before(first) {
			first = started
		}
		if ended := r.EndedAt(); ended.After(last) {
			last = ended
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
	if !last.IsZero() && last.After(first) {
		s.WindowNs = last.Sub(first).Nanoseconds()
		seconds := last.Sub(first).Seconds()
		s.ThroughputRPS = float64(s.Successes) / seconds
		s.GoodputRPS = float64(met) / seconds
	}

	slices.Sort(ttft)
	slices.Sort(total)
	slices.Sort(itl)
	s.TTFTP50Ns = stats.Quantile(ttft, 0.50).Nanoseconds()
	s.TTFTP95Ns = stats.Quantile(ttft, 0.95).Nanoseconds()
	s.TTFTP99Ns = stats.Quantile(ttft, 0.99).Nanoseconds()
	s.TotalP50Ns = stats.Quantile(total, 0.50).Nanoseconds()
	s.TotalP99Ns = stats.Quantile(total, 0.99).Nanoseconds()
	s.ITLP50Ns = stats.Quantile(itl, 0.50).Nanoseconds()
	s.ITLP95Ns = stats.Quantile(itl, 0.95).Nanoseconds()

	s.WarmupDrift = warmupDrift(results, first, last)
	s.flag(opts.driftThreshold())
	return s
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
func (s *Summary) flag(driftThreshold float64) {
	if s.Requests == 0 {
		s.add("cell produced no measured requests")
	}
	if s.FailureThreshold > 0 && s.FailureRate > s.FailureThreshold {
		s.add(fmt.Sprintf("failure rate %.2f%% exceeds the %.2f%% threshold (%d dropped, %d failed of %d)",
			s.FailureRate*100, s.FailureThreshold*100, s.Dropped, s.Failed, s.Requests))
	}
	if driftThreshold > 0 && s.WarmupDrift > driftThreshold {
		s.add(fmt.Sprintf("still warming up: the first half of the measured window was %.0f%% slower than the second by TTFT p50, over a %.0f%% threshold. Lengthen the warm-up and re-run",
			s.WarmupDrift*100, driftThreshold*100))
	}
	if s.Cancelled > 0 {
		// The driver lets in-flight requests finish, so a cancellation means
		// the cell was interrupted rather than run to its end.
		s.add(fmt.Sprintf("%d requests were cancelled: the cell did not run to completion", s.Cancelled))
	}
}

func (s *Summary) add(reason string) {
	s.Flagged = true
	s.FlagReasons = append(s.FlagReasons, reason)
}
