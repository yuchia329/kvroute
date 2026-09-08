package bench_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// prefilled is a cell that also carries what the fleet's GPUs actually had to
// compute over its window, and the prompt bytes it offered them.
func prefilled(c bench.Cell, promptTokens, cachedTokens float64, promptBytes int64) bench.Cell {
	c.PromptTokens, c.PromptTokensCached, c.PrefillRead = promptTokens, cachedTokens, true
	c.PromptBytes = promptBytes
	return c
}

// fourPolicyCells is the comparison #15 delivers: the three baselines plus
// prefix affinity, which keeps more of each conversation on the replica already
// holding it and therefore leaves the fleet recomputing fewer prompt tokens.
//
// Every policy offers the same prompt bytes, because the harness refuses to
// compare cells whose workloads differ. That is what makes the difference in
// recomputed prefill between them redundant prefill rather than a difference in
// how much work they were asked to do.
func fourPolicyCells(load bench.Load) []bench.Cell {
	const bytes = 3_800_000
	cells := threePolicyCells(load)
	for i := range cells {
		switch cells[i].Policy {
		case policy.RoundRobinName:
			cells[i] = prefilled(cells[i], 1_000_000, 100_000, bytes)
		case policy.LeastOutstandingName:
			cells[i] = prefilled(cells[i], 1_000_000, 120_000, bytes)
		case policy.SessionAffinityName:
			cells[i] = prefilled(cells[i], 1_000_000, 600_000, bytes)
		}
	}
	return append(cells,
		prefilled(measured(policy.PrefixAffinityName, load, 1, 12.0, 120*time.Millisecond, 400*time.Millisecond, 780, 1000), 1_000_000, 780_000, bytes),
		prefilled(measured(policy.PrefixAffinityName, load, 2, 12.4, 125*time.Millisecond, 410*time.Millisecond, 800, 1000), 1_000_000, 800_000, bytes),
	)
}

// The criterion: a four-policy table reporting goodput, prefix cache hit rate
// and redundant prefill. The first says which policy won, the second says
// whether it won on cache locality, and the third is the physical work that
// locality removed — the measurement of the mechanism rather than of the
// outcome.
func TestTheFourPolicyTableReportsGoodputHitRateAndRedundantPrefill(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	got := compare(t, fourPolicyCells(at8))

	if len(got.Policies) != 4 {
		t.Fatalf("comparison covers %v, want four policies", got.Policies)
	}
	if last := got.Policies[len(got.Policies)-1]; last != policy.PrefixAffinityName {
		t.Errorf("the last column is %q, want %q — the baselines come first", last, policy.PrefixAffinityName)
	}
	row := got.Rows[0]

	if g, ok := row.Goodput[policy.PrefixAffinityName]; !ok || g.MedianRPS < 12 {
		t.Errorf("prefix affinity's goodput is %v, want the median of 12.0 and 12.4", g)
	}
	if cache, ok := row.PrefixCache[policy.PrefixAffinityName]; !ok || cache.HitRate() < 0.7 {
		t.Errorf("prefix affinity's hit rate is %v, want the highest in the table", cache)
	}

	// Recomputed prefill, per policy, summed across repetitions the way the
	// cache counters are.
	prefill, ok := row.Prefill[policy.PrefixAffinityName]
	if !ok {
		t.Fatal("the table reports no prefill for prefix affinity")
	}
	if prefill.Recomputed() != 420_000 {
		t.Errorf("prefix affinity recomputed %v prompt tokens, want 420000 across its two repetitions", prefill.Recomputed())
	}

	// And the redundant part: what each policy computed over the policy that
	// computed least, on identical bytes.
	redundant, ok := row.Redundant(policy.RoundRobinName)
	if !ok {
		t.Fatal("round-robin's redundant prefill could not be derived")
	}
	if redundant != 1_380_000 {
		t.Errorf("round-robin's redundant prefill = %v, want 1380000 over prefix affinity", redundant)
	}
	if best, _ := row.Redundant(policy.PrefixAffinityName); best != 0 {
		t.Errorf("the policy that recomputed least carries %v redundant prefill, want none", best)
	}
}

// Redundant prefill is a comparison between policies, so it is meaningless
// unless every policy in the row was actually measured. One unread cell must
// make the column absent rather than let a scrape failure become a policy that
// looks like it recomputed nothing.
func TestOneUnreadPrefillMakesTheRedundantColumnAbsent(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	cells := fourPolicyCells(at8)
	for i := range cells {
		if cells[i].Policy == policy.PrefixAffinityName && cells[i].Repetition == 2 {
			cells[i].PrefillRead = false
		}
	}

	row := compare(t, cells).Rows[0]
	if p, ok := row.Prefill[policy.PrefixAffinityName]; ok && p.Read {
		t.Errorf("a policy with an unscraped repetition reported prefill: %v", p)
	}
	if _, ok := row.Redundant(policy.RoundRobinName); ok {
		t.Error("redundant prefill was derived against a policy whose own prefill was unread")
	}
}

// A prefix match is a length in bytes, and the ratio that converts it into the
// engine's units is measured rather than assumed. The report carries it because
// a byte figure nobody can convert is a figure nobody can check against the
// engine's own.
func TestTheReportPublishesTheMeasuredBytesPerTokenRatio(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	got := compare(t, fourPolicyCells(at8))

	ratio, ok := got.BytesPerToken()
	if !ok {
		t.Fatal("the comparison measured no bytes-per-token ratio")
	}
	// 3,800,000 bytes over 1,000,000 prompt tokens in each of eight cells.
	if math.Abs(ratio-3.8) > 1e-9 {
		t.Errorf("bytes per token = %v, want 3.8", ratio)
	}
	if report := got.Report(); !strings.Contains(report, "3.80 bytes per token") {
		t.Errorf("the report does not publish the ratio a prefix match is read through:\n%s", report)
	}
}

// The mechanism table has to name the work, not only the ratio. A hit rate says
// how often the cache answered; recomputed prefill says how many tokens the GPUs
// burned, and a policy can improve one without improving the other.
func TestTheReportPrintsRecomputedAndRedundantPrefillPerPolicy(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	report := compare(t, fourPolicyCells(at8)).Report()

	for _, want := range []string{"redundant prefill", "prompt tokens recomputed", policy.PrefixAffinityName} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
	// The policy that recomputed least is the baseline of the column, and it
	// carries no redundancy of its own.
	prefixLine := ""
	for _, line := range strings.Split(report, "\n") {
		if strings.Contains(line, policy.PrefixAffinityName) && strings.Contains(line, "|") && strings.Contains(line, "%") {
			prefixLine = line
		}
	}
	if prefixLine == "" {
		t.Fatalf("the mechanism table has no row for prefix affinity:\n%s", report)
	}
	if !strings.Contains(prefixLine, "best") {
		t.Errorf("the policy that recomputed least is not marked as the column's baseline: %q", prefixLine)
	}
}

// A cell whose engines were never scraped reports no prefill, rather than a
// fleet that computed nothing.
func TestACellWithNoPrefillEvidenceReportsNoneRatherThanZero(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	cells := fourPolicyCells(at8)
	for i := range cells {
		cells[i].PrefillRead = false
		cells[i].PromptTokens, cells[i].PromptTokensCached = 0, 0
	}

	got := compare(t, cells)
	if p, ok := got.Rows[0].Prefill[policy.RoundRobinName]; ok && p.Read {
		t.Errorf("an unscraped cell reported prefill: %v", p)
	}
	if _, ok := got.BytesPerToken(); ok {
		t.Error("a ratio was measured over cells whose token counts were never read")
	}
	if report := got.Report(); !strings.Contains(report, "—") {
		t.Errorf("the report prints no em dash for a figure it has no evidence for:\n%s", report)
	}
}
