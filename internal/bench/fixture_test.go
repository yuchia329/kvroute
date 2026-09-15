package bench_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// TestAFixtureSweepReadsBackToFiguresWorkedByHand reads a small sweep directory
// written by hand, whose every figure was worked out on paper before this test
// was, through the same loader and comparison every published table comes from.
//
// testdata/sweep holds, at 32 users:
//
//   - round robin, three clean repetitions of 4, 5 and 6 goodput/s
//   - prefix affinity at 10 and 12, clean; at 2.5, failing 5% of its requests
//     and flagged for that alone; at 99, unclean; and at 50, still warming up
//   - in discarded/, a contaminated cell of 1000 that a sweep set aside
//
// Nobody picks cells by hand on the way to a figure: the directory is read as a
// sweep left it, and the rules decide.
func TestAFixtureSweepReadsBackToFiguresWorkedByHand(t *testing.T) {
	cs, err := bench.LoadCells("testdata/sweep")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 8 {
		t.Fatalf("read %d cells, want the 8 under cells/ and not the one set aside in discarded/", len(cs))
	}

	got := compare(t, cs)
	row := got.Rows[0]

	rr := row.Goodput[policy.RoundRobinName]
	if rr.MedianRPS != 5.0 || rr.MinRPS != 4.0 || rr.MaxRPS != 6.0 || rr.Repetitions != 3 {
		t.Errorf("round robin = %+v, want median 5 over 4–6, n=3", rr)
	}
	prefix := row.Goodput[policy.PrefixAffinityName]
	if prefix.MedianRPS != 10.0 || prefix.MinRPS != 2.5 || prefix.MaxRPS != 12.0 || prefix.Repetitions != 3 || prefix.OverFailureThreshold != 1 {
		t.Errorf("prefix affinity = %+v, want median 10 over 2.5–12, n=3, one failing repetition: "+
			"the unclean 99 and the drifting 50 excluded, the failing 2.5 kept", prefix)
	}

	if len(got.Excluded) != 2 {
		t.Errorf("excluded %v, want the unclean and the drifting cell", got.Excluded)
	}
	if len(got.Surfaced) != 1 || !strings.Contains(got.Surfaced[0], "prefix_affinity-c32-r2") {
		t.Errorf("surfaced %v, want the cell that failed 5%% of its requests", got.Surfaced)
	}

	// Hit rates are pooled counts over the usable cells: 300 of 3,000 queries
	// against 2,400 of 3,000.
	if hit := row.PrefixCache[policy.RoundRobinName].HitRate(); hit != 0.1 {
		t.Errorf("round robin hit rate %v, want 0.1", hit)
	}
	if hit := row.PrefixCache[policy.PrefixAffinityName].HitRate(); hit != 0.8 {
		t.Errorf("prefix affinity hit rate %v, want 0.8", hit)
	}
	// Recomputed prefill: 3 × 900 against 3 × 200, over 300 requests each, so
	// round robin recomputes 9 tokens per request against 2 and carries 7 of them
	// over the floor — 2,100 tokens against its own requests.
	if excess, ok := row.RedundantPerRequest(policy.RoundRobinName); !ok || excess != 7 {
		t.Errorf("round robin redundant prefill = %v per request (%v), want 7", excess, ok)
	}
	if tokens, ok := row.RedundantTokens(policy.RoundRobinName); !ok || tokens != 2100 {
		t.Errorf("round robin redundant prefill = %v tokens (%v), want 2100", tokens, ok)
	}
	if excess, ok := row.RedundantPerRequest(policy.PrefixAffinityName); !ok || excess != 0 {
		t.Errorf("prefix affinity redundant prefill = %v (%v), want 0: it is the floor", excess, ok)
	}

	// 10 against 5 is +100%, but prefix affinity's 2.5–12 covers round robin's
	// 4–6, so it is not a separation.
	if !strings.Contains(got.Report(), "+100.0% (within spread)") {
		t.Errorf("the delta is not +100%% within spread:\n%s", got.Report())
	}
}
