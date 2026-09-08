package characterize

import (
	"fmt"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
)

// DefaultSLOMultiple is how many times the measured floor a request may cost
// and still count as met.
//
// Three, and stated rather than tuned. The SLO's job is to separate "the fleet
// is loaded" from "the fleet is failing this request", so it has to sit far
// enough above the floor that ordinary queueing does not trip it and close
// enough that a request nobody would wait for does. Three times an idle fleet's
// latency is the line: at the measured floor it puts TTFT just under a second,
// which is roughly where an interactive client stops feeling answered.
//
// It is a knob because it is a judgement. What is not a judgement is the floor
// it multiplies, which is why the multiple is published beside the floor and
// the alternatives are published beside both.
const DefaultSLOMultiple = 3.0

// SLOCandidateMultiples are published beside the derived SLO so the choice of
// multiple is visible as a choice rather than presented as a measurement.
var SLOCandidateMultiples = []float64{2, 3, 4, 5}

// MinFloorRequests is how many successful requests the floor needs before it is
// worth deriving an SLO from. Every published goodput figure rests on this
// number, so a floor taken from a handful of requests is flagged rather than
// quietly used.
const MinFloorRequests = 30

// Floor is what the hardware costs with nothing in the way: one request at a
// time, straight at a replica, no router and no competing load.
//
// It is measured rather than assumed because the SLO is derived from it. An SLO
// picked a priori measures the person who picked it; an SLO that is a stated
// multiple of this measures the fleet.
type Floor struct {
	// Concurrency is the load level the floor was taken at, which is one by
	// definition: any higher and queueing is in the number.
	Concurrency int `json:"concurrency"`
	// Replicas is how many replicas' rows were pooled. Pooled rather than taken
	// from the fastest: an SLO derived from the best card would be unmeetable
	// on the worst, and every replica serves the same traffic.
	Replicas int `json:"replicas"`

	// PrefixCache is how much of the floor's prompt work the replicas answered
	// out of their caches. A floor measured on prompts the replicas had already
	// seen is not a floor: it is the cache's latency, and on this fleet that is
	// seven times lower.
	PrefixCache PrefixCacheDelta `json:"prefix_cache"`
	// MaxPrefixHitRate is the limit that was applied to it.
	MaxPrefixHitRate float64 `json:"max_prefix_hit_rate"`

	bench.Summary `json:"summary"`
}

// NewFloor pools the concurrency-1 rows of every replica into one floor.
//
// The warm-up drift check is disabled here. It compares the first half of a
// measured window against the second, which is a statement about one probe;
// pooled across probes taken minutes apart in alternating replica order, the
// two halves are different replicas rather than the same one warming up. Each
// probe still runs the check on its own rows, which is where it means
// something.
func NewFloor(rows []bench.Result, replicas int, prefixCache PrefixCacheDelta, maxHitRate float64) Floor {
	if maxHitRate <= 0 {
		maxHitRate = MaxFloorPrefixHitRate
	}
	return Floor{
		Concurrency:      1,
		Replicas:         replicas,
		PrefixCache:      prefixCache,
		MaxPrefixHitRate: maxHitRate,
		Summary:          bench.Summarize(rows, pooledOptions),
	}
}

// pooledOptions summarise rows gathered across several probes: every check that
// is about one measured window is off, and the ones about the rows themselves
// stay on.
var pooledOptions = bench.SummaryOptions{WarmupDriftThreshold: -1}

// TTFT and ITL are the floor's headline figures: the median cost of a request
// on an idle fleet.
func (f Floor) TTFT() time.Duration { return time.Duration(f.TTFTP50Ns) }
func (f Floor) ITL() time.Duration  { return time.Duration(f.ITLP50Ns) }

// DerivedSLO is an SLO and the floor it came from, kept together because
// neither is interpretable alone.
type DerivedSLO struct {
	Multiple  float64       `json:"multiple"`
	TTFT      time.Duration `json:"ttft"`
	ITL       time.Duration `json:"itl"`
	FloorTTFT time.Duration `json:"floor_ttft"`
	FloorITL  time.Duration `json:"floor_itl"`
}

// SLO is the threshold in the form the harness takes.
func (d DerivedSLO) SLO() bench.SLO { return bench.SLO{TTFT: d.TTFT, ITL: d.ITL} }

// String states the SLO and its derivation in one line, so no table can carry
// the threshold without the floor it came from.
//
// The floor is rounded for reading only. What it was is in the record; a
// sentence carrying six decimal places of nanoseconds is a sentence nobody
// finishes.
func (d DerivedSLO) String() string {
	return fmt.Sprintf("TTFT < %v, inter-token p50 < %v (%.3gx the measured floor of %v and %v)",
		d.TTFT, d.ITL, d.Multiple, d.FloorTTFT.Round(time.Millisecond), d.FloorITL.Round(100*time.Microsecond))
}

// Derive turns the floor into an SLO at the given multiple.
//
// The median is what is multiplied, not the tail. The floor is what the
// hardware costs when nothing is in the way, and at concurrency one the p99
// already contains this fleet's own jitter — deriving from it would build that
// jitter into the threshold and hide exactly the degradation the SLO exists to
// catch.
//
// Both thresholds are rounded up: to 10 ms for TTFT and 1 ms for inter-token
// latency. Rounding up keeps the multiple a lower bound rather than a number
// the threshold might undercut, and it makes the SLO a figure that can be
// quoted without a decimal tail nobody can reproduce.
func (f Floor) Derive(multiple float64) DerivedSLO {
	if multiple <= 0 {
		multiple = DefaultSLOMultiple
	}
	return DerivedSLO{
		Multiple:  multiple,
		TTFT:      roundUp(scale(f.TTFT(), multiple), 10*time.Millisecond),
		ITL:       roundUp(scale(f.ITL(), multiple), time.Millisecond),
		FloorTTFT: f.TTFT(),
		FloorITL:  f.ITL(),
	}
}

// Candidates derives the SLO at each multiple, so the one chosen is visible
// beside the ones that were not.
func (f Floor) Candidates(multiples []float64) []DerivedSLO {
	out := make([]DerivedSLO, 0, len(multiples))
	for _, m := range multiples {
		out = append(out, f.Derive(m))
	}
	return out
}

// Usable reports whether the floor rests on enough successful requests to
// derive an SLO from, and why not when it does not.
func (f Floor) Usable() (bool, string) {
	switch {
	case f.Successes < MinFloorRequests:
		return false, fmt.Sprintf("the floor rests on %d successful requests, under the %d needed: every goodput figure would inherit that noise",
			f.Successes, MinFloorRequests)
	case f.TTFTP50Ns <= 0 || f.ITLP50Ns <= 0:
		return false, "the floor carries no TTFT or no inter-token latency, so there is nothing to derive an SLO from"
	case f.Flagged:
		return false, fmt.Sprintf("the floor's own rows are flagged: %v", f.FlagReasons)
	}
	// Checked last because it is the one that catches a floor that looks
	// perfect: hundreds of clean, fast, low-variance requests that were never
	// prefilled at all.
	if reason := prefixCacheReason(f.PrefixCache, f.MaxPrefixHitRate); reason != "" {
		return false, reason
	}
	return true, ""
}

func scale(d time.Duration, by float64) time.Duration {
	return time.Duration(float64(d) * by)
}

// roundUp rounds d up to the next whole unit, so a derived threshold is never
// below the multiple it claims.
func roundUp(d, unit time.Duration) time.Duration {
	if unit <= 0 || d%unit == 0 {
		return d
	}
	return (d/unit + 1) * unit
}
