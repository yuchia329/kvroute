package characterize_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/record"
)

// rows builds n successful concurrency-1 rows at the given latencies.
func rows(n int, ttft, itl time.Duration) []bench.Result {
	out := make([]bench.Result, 0, n)
	for i := range n {
		out = append(out, bench.Result{
			Labels:      bench.Labels{Concurrency: 1},
			Replica:     "replica-0",
			StartedAtNs: int64(i) * int64(time.Second),
			TTFTNs:      ttft.Nanoseconds(),
			ITLP50Ns:    itl.Nanoseconds(),
			TotalNs:     (ttft + 64*itl).Nanoseconds(),
			Outcome:     record.OutcomeSuccess,
		})
	}
	return out
}

// coldFloor builds a floor whose prompts the replicas had not seen: 1% of
// prompt tokens cached, which is the chat template's first block and nothing
// else.
func coldFloor(results []bench.Result, replicas int) characterize.Floor {
	return characterize.NewFloor(results, replicas,
		characterize.PrefixCacheDelta{Hits: 100, Queries: 10000, Read: true}, 0)
}

func TestTheFloorIsTheMedianOfEveryReplicasConcurrencyOneRows(t *testing.T) {
	floor := coldFloor(rows(60, 320*time.Millisecond, 8*time.Millisecond), 6)

	if floor.Concurrency != 1 {
		t.Errorf("floor concurrency = %d, want 1: any higher and queueing is in the number", floor.Concurrency)
	}
	if floor.Replicas != 6 {
		t.Errorf("floor pooled %d replicas, want 6", floor.Replicas)
	}
	if floor.TTFT() != 320*time.Millisecond || floor.ITL() != 8*time.Millisecond {
		t.Errorf("floor is %v / %v, want 320ms / 8ms", floor.TTFT(), floor.ITL())
	}
}

func TestTheSLOIsAStatedMultipleOfTheFloor(t *testing.T) {
	floor := coldFloor(rows(60, 320*time.Millisecond, 8*time.Millisecond), 6)

	slo := floor.Derive(3)
	if slo.TTFT != 960*time.Millisecond {
		t.Errorf("TTFT SLO = %v, want 960ms: three times the 320ms floor", slo.TTFT)
	}
	if slo.ITL != 24*time.Millisecond {
		t.Errorf("ITL SLO = %v, want 24ms: three times the 8ms floor", slo.ITL)
	}
	// The floor travels with the threshold. A threshold quoted without it is a
	// number somebody chose.
	if slo.FloorTTFT != 320*time.Millisecond || slo.FloorITL != 8*time.Millisecond {
		t.Errorf("derived SLO %+v does not carry the floor it came from", slo)
	}
	if got := slo.SLO(); got.TTFT != slo.TTFT || got.ITL != slo.ITL {
		t.Errorf("harness SLO %+v disagrees with the derived one %+v", got, slo)
	}
}

// Rounding up keeps the multiple a floor rather than a number the threshold
// might undercut, and keeps the published figure quotable.
func TestDerivedThresholdsRoundUpSoTheyNeverUndercutTheStatedMultiple(t *testing.T) {
	floor := coldFloor(rows(60, 327*time.Millisecond, 7600*time.Microsecond), 6)

	slo := floor.Derive(3)
	if slo.TTFT != 990*time.Millisecond {
		t.Errorf("TTFT SLO = %v, want 990ms: 981ms rounded up to 10ms", slo.TTFT)
	}
	if slo.ITL != 23*time.Millisecond {
		t.Errorf("ITL SLO = %v, want 23ms: 22.8ms rounded up to 1ms", slo.ITL)
	}
	if slo.TTFT < time.Duration(3*float64(327*time.Millisecond)) {
		t.Error("rounding took the threshold below the multiple it claims")
	}
}

func TestCandidateMultiplesArePublishedBesideTheChosenOne(t *testing.T) {
	floor := coldFloor(rows(60, 320*time.Millisecond, 8*time.Millisecond), 6)

	candidates := floor.Candidates(characterize.SLOCandidateMultiples)
	if len(candidates) != len(characterize.SLOCandidateMultiples) {
		t.Fatalf("got %d candidates, want %d", len(candidates), len(characterize.SLOCandidateMultiples))
	}
	for i := 1; i < len(candidates); i++ {
		if candidates[i].TTFT <= candidates[i-1].TTFT {
			t.Errorf("candidate at %gx is not looser than at %gx", candidates[i].Multiple, candidates[i-1].Multiple)
		}
	}
}

// Every goodput figure the project publishes rests on this threshold, so a
// floor taken from a handful of requests has to say so rather than be used.
func TestAFloorWithTooFewRequestsRefusesToBeUsed(t *testing.T) {
	ok, why := coldFloor(rows(5, 320*time.Millisecond, 8*time.Millisecond), 6).Usable()
	if ok {
		t.Fatal("a floor of five requests reported itself usable")
	}
	if why == "" {
		t.Error("an unusable floor gave no reason")
	}

	if ok, why := coldFloor(rows(60, 320*time.Millisecond, 8*time.Millisecond), 6).Usable(); !ok {
		t.Errorf("a floor of sixty clean requests was refused: %s", why)
	}
}

// A cell that produced no successful response has no latency in it, and an SLO
// derived from a zero floor would pass everything.
func TestAFloorWithNoSuccessfulRowsRefusesToBeUsed(t *testing.T) {
	if ok, _ := coldFloor(nil, 6).Usable(); ok {
		t.Fatal("an empty floor reported itself usable")
	}
}

// The failure this exists to catch, and the one that looks like success: a
// floor built from hundreds of clean, fast, low-variance requests that the
// replicas never actually prefilled, because a previous run had already sent
// those exact prompts and they came back out of the prefix cache. Measured on
// the box, that is 46 ms standing in for 325 ms.
func TestAFloorMeasuredAgainstAWarmPrefixCacheRefusesToBeUsed(t *testing.T) {
	warm := characterize.NewFloor(rows(200, 46*time.Millisecond, 8*time.Millisecond), 6,
		characterize.PrefixCacheDelta{Hits: 9000, Queries: 10000, Read: true}, 0)

	ok, why := warm.Usable()
	if ok {
		t.Fatal("a floor measured entirely out of the prefix cache reported itself usable")
	}
	if !strings.Contains(why, "prefix cache") {
		t.Errorf("the reason given was %q, which does not say the cache was what was measured", why)
	}
}

// "The cache was cold" and "nobody read the counters" must not read the same.
func TestAFloorWithNoPrefixCacheEvidenceRefusesToBeUsed(t *testing.T) {
	if ok, _ := characterize.NewFloor(rows(200, 320*time.Millisecond, 8*time.Millisecond), 6,
		characterize.PrefixCacheDelta{}, 0).Usable(); ok {
		t.Fatal("a floor with no cache evidence reported itself usable")
	}
}
