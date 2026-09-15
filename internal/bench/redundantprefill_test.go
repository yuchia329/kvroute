package bench_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// served is a cell that recomputed a given number of prompt tokens over a given
// number of measured requests, at a fixed prompt size.
//
// The prompt size is shared because the two policies send the same workload —
// the comparison refuses cells whose workloads differ — so a cell that served
// twice the requests offered twice the prompt tokens. That is the whole of the
// bias this file is about: the same generator does not mean the same number of
// prompts.
func served(c bench.Cell, requests int, recomputed float64) bench.Cell {
	const promptTokensPerRequest = 2048
	c.Requests, c.Successes = requests, requests
	c.PromptTokens = float64(requests) * promptTokensPerRequest
	c.PromptTokensCached = c.PromptTokens - recomputed
	c.PrefillRead = true
	return c
}

// closedLoopWasteCells are #18's WS 3 / skew 1.4 point, rounded to the figures
// issue #30 tabulates: prefix affinity answers faster, so the closed-loop driver
// gets further through the same sequence and it serves 21,824 requests against
// session affinity's 12,188 — and it recomputes 157 prompt tokens per request
// against 168, while recomputing 3.4M tokens in total against 2.0M.
//
// The faster policy wastes less and computes more. A column built on absolute
// totals therefore reverses the verdict, which is the defect under test.
func closedLoopWasteCells(load bench.Load) []bench.Cell {
	session := measured(policy.SessionAffinityName, load, 1, 10.0, 150*time.Millisecond, 500*time.Millisecond, 600, 1000)
	prefix := measured(policy.PrefixAffinityName, load, 1, 17.0, 120*time.Millisecond, 400*time.Millisecond, 780, 1000)
	return []bench.Cell{
		served(session, 12_188, 12_188*168),
		served(prefix, 21_824, 21_824*157),
	}
}

// The criterion: redundant prefill is per request, so the policy that recomputed
// least per request is the floor the column is measured from — even where it
// recomputed more tokens in total, because it served more requests to do it.
func TestRedundantPrefillCreditsTheFasterPolicyThatWastesLessPerRequest(t *testing.T) {
	at32 := bench.ClosedLoopAt(32)
	got := compare(t, closedLoopWasteCells(at32))
	row := got.Rows[0]

	// The premise: absolute recomputed prefill runs the other way. If this ever
	// stops holding, the fixture has stopped exercising the bias.
	session, prefix := row.Prefill[policy.SessionAffinityName], row.Prefill[policy.PrefixAffinityName]
	if prefix.Recomputed() <= session.Recomputed() {
		t.Fatalf("fixture: prefix affinity recomputed %.0f tokens and session affinity %.0f — the faster policy has to compute more in total for this test to mean anything",
			prefix.Recomputed(), session.Recomputed())
	}

	perRequest, ok := prefix.RecomputedPerRequest()
	if !ok || perRequest != 157 {
		t.Errorf("prefix affinity recomputed %v prompt tokens per request (%v), want 157", perRequest, ok)
	}
	if perRequest, ok := session.RecomputedPerRequest(); !ok || perRequest != 168 {
		t.Errorf("session affinity recomputed %v prompt tokens per request (%v), want 168", perRequest, ok)
	}

	// The floor is the policy that wasted least per request, which is the faster
	// one here.
	if excess, ok := row.RedundantPerRequest(policy.PrefixAffinityName); !ok || excess != 0 {
		t.Errorf("prefix affinity carries %v redundant prefill per request (%v), want none: it is the floor", excess, ok)
	}
	if excess, ok := row.RedundantPerRequest(policy.SessionAffinityName); !ok || excess != 11 {
		t.Errorf("session affinity carries %v redundant prefill per request (%v), want 168 − 157 = 11", excess, ok)
	}

	// A token total is that excess against the policy's *own* requests, never a
	// difference of two policies' totals.
	if tokens, ok := row.RedundantTokens(policy.SessionAffinityName); !ok || tokens != 11*12_188 {
		t.Errorf("session affinity's redundant prefill = %v tokens (%v), want 11 × its own 12,188 requests", tokens, ok)
	}
	if tokens, ok := row.RedundantTokens(policy.PrefixAffinityName); !ok || tokens != 0 {
		t.Errorf("the floor carries %v redundant tokens, want none", tokens)
	}
}

// And the report says so: the faster policy is marked best, and every token
// total has the per-request figure beside it, so a reader can tell a policy that
// served more requests from one that wasted more on each.
func TestTheReportCreditsTheFasterPolicyAndPrintsPerRequestBesideTheTotal(t *testing.T) {
	report := compare(t, closedLoopWasteCells(bench.ClosedLoopAt(32))).Report()

	for _, want := range []string{"recomputed / request", "redundant prefill / request", "requests"} {
		if !strings.Contains(report, want) {
			t.Errorf("the mechanism table has no %q column:\n%s", want, report)
		}
	}
	prefixLine, sessionLine := mechanismLine(t, report, policy.PrefixAffinityName), mechanismLine(t, report, policy.SessionAffinityName)
	if !strings.Contains(prefixLine, "best") {
		t.Errorf("the policy that wasted least per request is not the column's floor: %q", prefixLine)
	}
	if strings.Contains(sessionLine, "best") {
		t.Errorf("the policy that wasted more per request was credited as the floor: %q", sessionLine)
	}
	if !strings.Contains(sessionLine, "+11") {
		t.Errorf("session affinity's row does not carry its 11 tokens per request of excess: %q", sessionLine)
	}
}

// mechanismLine is a policy's row of the report's mechanism table, found by the
// hit rate only that table prints.
func mechanismLine(t *testing.T, report, name string) string {
	t.Helper()
	for _, line := range strings.Split(report, "\n") {
		if strings.Contains(line, name) && strings.Contains(line, "|") && strings.Contains(line, "%") {
			return line
		}
	}
	t.Fatalf("the mechanism table has no row for %s:\n%s", name, report)
	return ""
}

// A ratio needs its denominator. A policy whose cells recorded no measured
// request has no per-request figure, and a row holding one cannot report the
// column at all — the floor would be taken over an incomplete set, which is the
// same defect an unread counter causes.
func TestRedundantPrefillNeedsRequestsToDivideBy(t *testing.T) {
	at32 := bench.ClosedLoopAt(32)
	cells := closedLoopWasteCells(at32)
	for i := range cells {
		if cells[i].Policy == policy.PrefixAffinityName {
			cells[i].Requests, cells[i].Successes = 0, 0
		}
	}

	row := compare(t, cells).Rows[0]
	if _, ok := row.Prefill[policy.PrefixAffinityName].RecomputedPerRequest(); ok {
		t.Error("a policy that served no measured request reported prompt tokens per request")
	}
	if excess, ok := row.RedundantPerRequest(policy.SessionAffinityName); ok {
		t.Errorf("redundant prefill of %v per request was measured against a floor with no denominator", excess)
	}
}
