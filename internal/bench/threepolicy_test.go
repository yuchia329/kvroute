package bench_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// measured is a cell with the figures the three-policy table reports beyond
// goodput: the TTFT percentiles the fleet produced, and the prefix-cache
// counters it moved over the cell's own window.
func measured(policyName string, load bench.Load, repetition int, goodput float64, ttftP50, ttftP99 time.Duration, hits, queries float64) bench.Cell {
	c := cell(policyName, load, repetition, goodput)
	c.TTFTP50Ns = ttftP50.Nanoseconds()
	c.TTFTP99Ns = ttftP99.Nanoseconds()
	c.PrefixCacheHits, c.PrefixCacheQueries, c.PrefixCacheRead = hits, queries, true
	return c
}

// threePolicyCells is the shape of the comparison #14 delivers: round-robin,
// least-outstanding and session affinity over one load point, with affinity
// keeping conversations warm and therefore hitting cache far more often.
func threePolicyCells(load bench.Load) []bench.Cell {
	return []bench.Cell{
		measured(policy.RoundRobinName, load, 1, 8.0, 300*time.Millisecond, 900*time.Millisecond, 100, 1000),
		measured(policy.RoundRobinName, load, 2, 8.2, 310*time.Millisecond, 920*time.Millisecond, 110, 1000),
		measured(policy.LeastOutstandingName, load, 1, 9.0, 280*time.Millisecond, 700*time.Millisecond, 120, 1000),
		measured(policy.LeastOutstandingName, load, 2, 9.4, 290*time.Millisecond, 720*time.Millisecond, 130, 1000),
		measured(policy.SessionAffinityName, load, 1, 10.0, 150*time.Millisecond, 500*time.Millisecond, 600, 1000),
		measured(policy.SessionAffinityName, load, 2, 10.6, 160*time.Millisecond, 520*time.Millisecond, 620, 1000),
	}
}

// The criterion: a three-policy table reporting goodput, TTFT percentiles and
// prefix cache hit rate. Goodput alone cannot say why a policy won, and the
// project's whole claim is about cache locality, so the figure that is ground
// truth for locality has to be in the same table as the outcome.
func TestTheThreePolicyTableReportsGoodputTTFTPercentilesAndPrefixCacheHitRate(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	got := compare(t, threePolicyCells(at8))

	if len(got.Policies) != 3 {
		t.Fatalf("comparison covers %v, want three policies", got.Policies)
	}
	if len(got.Rows) != 1 {
		t.Fatalf("comparison has %d rows, want one load point", len(got.Rows))
	}
	row := got.Rows[0]

	// Goodput, pooled the way it always was.
	if g, ok := row.Goodput[policy.SessionAffinityName]; !ok || g.MedianRPS < 10 {
		t.Errorf("session affinity's goodput is %v, want the median of 10.0 and 10.6", g)
	}

	// TTFT percentiles, per policy.
	ttft, ok := row.Latency[policy.SessionAffinityName]
	if !ok {
		t.Fatalf("no TTFT figures for session affinity: row has %v", row.Latency)
	}
	// stats.Quantile is the project's one definition of a percentile and picks by
	// rank, so two repetitions give the lower of the pair — the same rule the
	// goodput beside it is pooled by, which is what keeps the two commensurable.
	if want := 150 * time.Millisecond; time.Duration(ttft.P50Ns) != want {
		t.Errorf("session affinity TTFT p50 is %v, want %v: the median of its repetitions' p50s", time.Duration(ttft.P50Ns), want)
	}
	if want := 500 * time.Millisecond; time.Duration(ttft.P99Ns) != want {
		t.Errorf("session affinity TTFT p99 is %v, want %v", time.Duration(ttft.P99Ns), want)
	}
	if baseline := row.Latency[policy.RoundRobinName]; baseline.P99Ns <= ttft.P99Ns {
		t.Errorf("round-robin p99 %v is not above session affinity's %v, so the fixture does not separate them",
			time.Duration(baseline.P99Ns), time.Duration(ttft.P99Ns))
	}

	// Prefix cache prefix cache hit rate: counts summed across the pooled repetitions, which
	// is the only way to combine a ratio, and the rate derived from them.
	cache, ok := row.PrefixCache[policy.SessionAffinityName]
	if !ok {
		t.Fatalf("no prefix-cache figure for session affinity")
	}
	if cache.Hits != 1220 || cache.Queries != 2000 {
		t.Errorf("session affinity pooled %v hits over %v queries, want 1220 over 2000", cache.Hits, cache.Queries)
	}
	if want := 0.61; cache.HitRate() != want {
		t.Errorf("session affinity prefix cache hit rate is %v, want %v", cache.HitRate(), want)
	}
	if baseline := row.PrefixCache[policy.RoundRobinName]; baseline.HitRate() >= cache.HitRate() {
		t.Errorf("round-robin prefix cache hit rate %v is not below session affinity's %v", baseline.HitRate(), cache.HitRate())
	}
}

// The report has to actually print them, in idea.md §5's policy order with the
// naive baseline first, or the table is a struct nobody reads.
func TestTheReportPrintsEveryPolicysLatencyAndPrefixCacheHitRate(t *testing.T) {
	got := compare(t, threePolicyCells(bench.ClosedLoopAt(8))).Report()

	for _, want := range []string{
		policy.RoundRobinName, policy.LeastOutstandingName, policy.SessionAffinityName,
		"TTFT p50", "TTFT p99", "prefix cache hit rate",
		"150ms", "500ms", "61.0%",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not mention %q:\n%s", want, got)
		}
	}

	// The baseline leads, whichever order the runs happened in.
	rr := strings.Index(got, policy.RoundRobinName)
	affinity := strings.Index(got, policy.SessionAffinityName)
	if rr < 0 || affinity < 0 || rr > affinity {
		t.Errorf("session affinity is reported before the round-robin baseline")
	}
}

// A policy whose cells carry no readable counters gets no prefix cache hit rate rather than a
// zero. Zero is a real reading of a cache that never hit, and printing it for a
// scrape that never happened would put a missing measurement in a published
// column with nothing admitting to it.
func TestAPolicyWithNoPrefixCacheEvidenceReportsNoneRatherThanZero(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	cs := threePolicyCells(at8)
	for i := range cs {
		if cs[i].Policy == policy.RoundRobinName {
			cs[i].PrefixCacheHits, cs[i].PrefixCacheQueries, cs[i].PrefixCacheRead = 0, 0, false
		}
	}

	got := compare(t, cs)
	if cache, ok := got.Rows[0].PrefixCache[policy.RoundRobinName]; ok && cache.Evidenced() {
		t.Errorf("round-robin has no readable counters but reports the prefix cache hit rate %v", cache)
	}

	report := got.Report()
	if strings.Contains(report, "0.0%") {
		t.Errorf("a policy with no prefix-cache evidence was printed as 0.0%%:\n%s", report)
	}
}

// One repetition scraped and one not makes the policy's pooled figure unread. A
// prefix cache hit rate over cells where some were checked and some were not is a number
// nobody can say what is behind.
func TestOneUnscrapedRepetitionMakesThePolicysPrefixCacheHitRateUnread(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	cs := threePolicyCells(at8)
	for i := range cs {
		if cs[i].Policy == policy.SessionAffinityName && cs[i].Repetition == 2 {
			cs[i].PrefixCacheRead = false
		}
	}

	got := compare(t, cs)
	if cache, ok := got.Rows[0].PrefixCache[policy.SessionAffinityName]; ok && cache.Read {
		t.Errorf("one of two repetitions was unscraped but the policy reports the reading %v", cache)
	}
}
