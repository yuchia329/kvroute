package bench_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
)

// The SLO every cell in these tests was judged against: the one the
// characterization derived on 2026-09-07.
var derivedSLO = bench.SLO{TTFT: 990 * time.Millisecond, ITL: 24 * time.Millisecond}

// cell builds a completed, clean, unflagged cell at one load point.
func cell(policyName string, load bench.Load, repetition int, goodput float64) bench.Cell {
	return bench.Cell{
		ID:          bench.CellID(policyName, load, repetition),
		Policy:      policyName,
		Driver:      load.Driver,
		Concurrency: load.Concurrency,
		ArrivalRate: load.ArrivalRate,
		Repetition:  repetition,
		Workload:    "fixed(prompt=2048B,output=64t,seed=1)",
		Summary: bench.Summary{
			Requests: 100, Successes: 100,
			GoodputRPS: goodput, ThroughputRPS: goodput + 1,
			SLOApplied: true,
			SLOTTFTNs:  derivedSLO.TTFT.Nanoseconds(),
			SLOITLNs:   derivedSLO.ITL.Nanoseconds(),
		},
		Contamination: bench.Contamination{GPUSamples: 12, Clean: true},
	}
}

// cells builds one policy's repetitions at one load point.
func cells(policyName string, load bench.Load, goodputs ...float64) []bench.Cell {
	out := make([]bench.Cell, 0, len(goodputs))
	for i, g := range goodputs {
		out = append(out, cell(policyName, load, i+1, g))
	}
	return out
}

func compare(t *testing.T, cs []bench.Cell) bench.Comparison {
	t.Helper()
	got, err := bench.Compare(cs)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	return got
}

// The P90 is pooled the way the P50 and P99 beside it are — the median of the
// repetitions' own P90s — and it gets a column of its own, because it is the
// figure llm-d published its precise-versus-approximate result in.
func TestTheComparisonReportsTheNinetiethPercentileTTFT(t *testing.T) {
	load := bench.ClosedLoopAt(32)
	var cs []bench.Cell
	for i, p90 := range []time.Duration{400 * time.Millisecond, 600 * time.Millisecond, 500 * time.Millisecond} {
		c := cell(policy.PrefixAffinityName, load, i+1, 10)
		c.TTFTP90Ns = p90.Nanoseconds()
		cs = append(cs, c)
	}
	cs = append(cs, cells(policy.SessionAffinityName, load, 9, 9, 9)...)

	got := compare(t, cs)

	if p90 := time.Duration(got.Rows[0].Latency[policy.PrefixAffinityName].P90Ns); p90 != 500*time.Millisecond {
		t.Errorf("pooled TTFT p90 is %v, want 500ms: the median of its repetitions' p90s", p90)
	}
	if !strings.Contains(got.Report(), "TTFT p90") {
		t.Error("the report has no TTFT p90 column")
	}
}

// TestTheComparisonReportsGoodputForEveryPolicyAtEveryLoadPoint is the
// deliverable's shape: the two policies' goodput against the derived SLO, side by
// side, at each point of the load axis.
func TestTheComparisonReportsGoodputForEveryPolicyAtEveryLoadPoint(t *testing.T) {
	at8, at16 := bench.ClosedLoopAt(8), bench.ClosedLoopAt(16)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0, 8.2, 8.4)...)
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 9.0, 9.4, 9.8)...)
	cs = append(cs, cells(policy.RoundRobinName, at16, 10.0, 10.0, 10.0)...)
	cs = append(cs, cells(policy.LeastOutstandingName, at16, 12.0, 12.0, 12.0)...)

	got := compare(t, cs)

	if len(got.Rows) != 2 {
		t.Fatalf("%d rows, want one per load point (8 and 16 users)", len(got.Rows))
	}
	if got.SLO != derivedSLO {
		t.Errorf("SLO = %+v, want the one the cells were judged against %+v", got.SLO, derivedSLO)
	}
	rr := got.Rows[0].Goodput[policy.RoundRobinName]
	if rr.MedianRPS != 8.2 {
		t.Errorf("round-robin median = %v, want the middle repetition 8.2", rr.MedianRPS)
	}
	if rr.MinRPS != 8.0 || rr.MaxRPS != 8.4 {
		t.Errorf("round-robin range = %v–%v, want 8.0–8.4", rr.MinRPS, rr.MaxRPS)
	}
	if rr.Repetitions != 3 {
		t.Errorf("round-robin pooled %d repetitions, want 3", rr.Repetitions)
	}

	report := got.Report()
	for _, want := range []string{
		"990ms",                      // the SLO the goodput is against
		policy.RoundRobinName,        // a column each
		policy.LeastOutstandingName,  //
		"8.20 (8.00–8.40, n=3)",      // median with the spread it came from
		"| closed-loop | 8 users |",  // the driver, on every row
		"| closed-loop | 16 users |", //
		"+20.0%",                     // 12.0 against 10.0 at 16 users
		"fixed(prompt=2048B,output=64t,seed=1)",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not carry %q:\n%s", want, report)
		}
	}
}

// TestTheBaselineIsFirstWhicheverOrderTheRunsHappenedIn. The Δ column is signed
// against the baseline, so a table that ordered its columns by which sweep was
// read first would flip the sign of its own result.
func TestTheBaselineIsFirstWhicheverOrderTheRunsHappenedIn(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	var cs []bench.Cell
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 9.0, 9.0, 9.0)...)
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0, 8.0, 8.0)...)

	got := compare(t, cs)

	if got.Policies[0] != policy.RoundRobinName {
		t.Errorf("baseline = %q, want %q: the naive baseline is the first column however the cells arrive",
			got.Policies[0], policy.RoundRobinName)
	}
	if !strings.Contains(got.Report(), "+12.5%") {
		t.Errorf("the delta is not signed against round-robin:\n%s", got.Report())
	}
}

// TestADifferenceInsideTheRepetitionSpreadIsNotReportedAsAWin. Run-to-run spread
// on a shared box is the thing most likely to be mistaken for a result, so a
// difference smaller than it is labelled rather than left to be read as a finding.
func TestADifferenceInsideTheRepetitionSpreadIsNotReportedAsAWin(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	var cs []bench.Cell
	// The medians differ by 2.4%, and each policy's own runs vary by more.
	cs = append(cs, cells(policy.RoundRobinName, at8, 7.5, 8.2, 9.0)...)
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 7.8, 8.4, 9.2)...)

	report := compare(t, cs).Report()

	if !strings.Contains(report, "within spread") {
		t.Errorf("a difference inside the repetition ranges is not marked as such:\n%s", report)
	}
}

// TestASinglePolicyIsNotAComparison.
func TestASinglePolicyIsNotAComparison(t *testing.T) {
	_, err := bench.Compare(cells(policy.RoundRobinName, bench.ClosedLoopAt(8), 8.0, 8.2, 8.4))
	if err == nil {
		t.Fatal("comparing one policy against itself returned no error")
	}
	if !strings.Contains(err.Error(), "at least two") {
		t.Errorf("err = %v, want it to say a comparison needs two policies", err)
	}
}

// TestCellsRunWithoutAnSLOHaveNoGoodputToCompare. Goodput is requests per second
// that met the SLO; without one there is nothing in that column but throughput
// wearing its name.
func TestCellsRunWithoutAnSLOHaveNoGoodputToCompare(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	unjudged := cell(policy.RoundRobinName, at8, 1, 8.0)
	unjudged.SLOApplied, unjudged.SLOTTFTNs, unjudged.SLOITLNs = false, 0, 0

	_, err := bench.Compare(append(cells(policy.LeastOutstandingName, at8, 9.0), unjudged))
	if err == nil {
		t.Fatal("comparing a cell with no SLO returned no error")
	}
	if !strings.Contains(err.Error(), "without an SLO") {
		t.Errorf("err = %v, want it to name the missing SLO", err)
	}
}

// TestTwoDifferentSLOsAreNotOneComparison: goodput is defined by the SLO, so two
// SLOs are two definitions of the primary metric.
func TestTwoDifferentSLOsAreNotOneComparison(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	other := cell(policy.LeastOutstandingName, at8, 1, 9.0)
	other.SLOTTFTNs = (2 * time.Second).Nanoseconds()

	_, err := bench.Compare(append(cells(policy.RoundRobinName, at8, 8.0), other))
	if err == nil {
		t.Fatal("comparing cells judged against two SLOs returned no error")
	}
	if !strings.Contains(err.Error(), "not comparable") {
		t.Errorf("err = %v, want it to refuse the comparison", err)
	}
}

// TestTwoDifferentWorkloadsAreNotOneComparison: the same load point under two
// policies has to send the same bytes, or the difference between the policies
// includes a difference in prompts. Same shape, different seed, which is the way
// this goes wrong in practice — the prompts differ and nothing about the two runs
// looks unalike.
func TestTwoDifferentWorkloadsAreNotOneComparison(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	other := cell(policy.LeastOutstandingName, at8, 1, 9.0)
	other.Workload = "fixed(prompt=2048B,output=64t,seed=7)"

	_, err := bench.Compare(append(cells(policy.RoundRobinName, at8, 8.0), other))
	if err == nil {
		t.Fatal("comparing cells that sent different workloads returned no error")
	}
	if !strings.Contains(err.Error(), "difference in prompts") {
		t.Errorf("err = %v, want it to name the workload difference", err)
	}
}

// TestFlaggedCellsAreLeftOutOfTheFiguresAndSaidSo. §6 discards a contaminated or
// broken cell rather than averaging it in, and a comparison that dropped it
// silently would be the same table with the evidence removed.
func TestFlaggedCellsAreLeftOutOfTheFiguresAndSaidSo(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	dirty := cell(policy.LeastOutstandingName, at8, 2, 99.0)
	dirty.Flagged = true
	dirty.FlagReasons = []string{"still warming up: the first half of the measured window was 40% slower than the second"}

	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0, 8.0, 8.0)...)
	cs = append(cs, cell(policy.LeastOutstandingName, at8, 1, 9.0), dirty)

	got := compare(t, cs)

	pooled := got.Rows[0].Goodput[policy.LeastOutstandingName]
	if pooled.Repetitions != 1 || pooled.MaxRPS == 99.0 {
		t.Errorf("the flagged cell was averaged in: pooled %d repetitions, max %v", pooled.Repetitions, pooled.MaxRPS)
	}
	report := got.Report()
	if !strings.Contains(report, "still warming up") {
		t.Errorf("the excluded cell's reason is not reported:\n%s", report)
	}
	if !strings.Contains(report, "(n=1)") {
		t.Errorf("a figure resting on one repetition does not say so:\n%s", report)
	}
}

// TestAnUncleanCellWithNoFlagIsStillExcluded. Contamination keeps Clean as its own
// field so that "nothing was found" and "nothing was looked for" cannot be
// collapsed; a comparison that inferred cleanliness from the flags would pool a
// record where the two disagree.
func TestAnUncleanCellWithNoFlagIsStillExcluded(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	unclean := cell(policy.LeastOutstandingName, at8, 2, 99.0)
	unclean.Clean = false // and deliberately not flagged

	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0)...)
	cs = append(cs, cell(policy.LeastOutstandingName, at8, 1, 9.0), unclean)

	got := compare(t, cs)

	pooled := got.Rows[0].Goodput[policy.LeastOutstandingName]
	if pooled.Repetitions != 1 || pooled.MaxRPS == 99.0 {
		t.Errorf("an unclean cell was averaged in: pooled %d repetitions, max %v", pooled.Repetitions, pooled.MaxRPS)
	}
	if !strings.Contains(got.Report(), "not clean and carries no flag") {
		t.Errorf("the report does not say the cell was excluded for being unclean:\n%s", got.Report())
	}
}

// pastFailureThreshold marks a cell as having dropped or failed more of its
// requests than the threshold allows, as Summarize marks one, and flags it for
// that and nothing else.
func pastFailureThreshold(c bench.Cell) bench.Cell {
	c.Failed = 12
	c.FailureRate, c.FailureThreshold = 0.12, 0.01
	c.Flag("failure rate 12.00% exceeds the 1.00% threshold (0 dropped, 12 failed of 100)")
	return c
}

// failing builds a clean cell whose only flag is that one.
func failing(policyName string, load bench.Load, repetition int, goodput float64) bench.Cell {
	return pastFailureThreshold(cell(policyName, load, repetition, goodput))
}

// TestACellThatFailedTooOftenStaysInTheFiguresMarked. A cell past the failure
// threshold is a measurement of a fleet that was falling over, not a broken
// measurement: leaving it out would turn a policy that collapses at high load into
// a gap in the table, which reads as "did not run" rather than "failed". So it is
// pooled, marked, and named — surfaced rather than dropped.
func TestACellThatFailedTooOftenStaysInTheFiguresMarked(t *testing.T) {
	at128 := bench.ClosedLoopAt(128)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at128, 8.0, 8.0, 8.0)...)
	cs = append(cs,
		cell(policy.LeastOutstandingName, at128, 1, 9.0),
		failing(policy.LeastOutstandingName, at128, 2, 3.0),
		cell(policy.LeastOutstandingName, at128, 3, 10.0))

	got := compare(t, cs)

	pooled := got.Rows[0].Goodput[policy.LeastOutstandingName]
	if pooled.Repetitions != 3 || pooled.MinRPS != 3.0 {
		t.Errorf("the failing cell was dropped: pooled %d repetitions, min %v, want 3 and 3.0", pooled.Repetitions, pooled.MinRPS)
	}
	if pooled.OverFailureThreshold != 1 {
		t.Errorf("%d pooled repetitions are marked as failing past the threshold, want 1", pooled.OverFailureThreshold)
	}
	if len(got.Excluded) != 0 {
		t.Errorf("the failing cell is listed as excluded: %v", got.Excluded)
	}
	report := got.Report()
	if !strings.Contains(report, "9.00 (3.00–10.00, n=3) ⚠") {
		t.Errorf("the figure resting on a failing cell is not marked:\n%s", report)
	}
	if !strings.Contains(report, "`least_outstanding-c128-r2`: failure rate 12.00%") {
		t.Errorf("the failing cell is not named with its reason:\n%s", report)
	}
}

// TestACellThatFailedAndIsFlaggedForSomethingElseIsExcluded. Surfacing is for a
// cell whose only defect is that the fleet failed requests. One that also warmed
// up too little, or saw a foreign process, is a broken measurement whatever else
// it shows, and §6 discards it.
func TestACellThatFailedAndIsFlaggedForSomethingElseIsExcluded(t *testing.T) {
	at128 := bench.ClosedLoopAt(128)
	drifting := failing(policy.LeastOutstandingName, at128, 2, 3.0)
	drifting.Flag("still warming up: the first half of the measured window was 40% slower than the second")
	unclean := failing(policy.LeastOutstandingName, at128, 3, 4.0)
	unclean.Clean = false

	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at128, 8.0)...)
	cs = append(cs, cell(policy.LeastOutstandingName, at128, 1, 9.0), drifting, unclean)

	got := compare(t, cs)

	pooled := got.Rows[0].Goodput[policy.LeastOutstandingName]
	if pooled.Repetitions != 1 || pooled.OverFailureThreshold != 0 {
		t.Errorf("pooled %d repetitions (%d marked failing), want only the one sound cell", pooled.Repetitions, pooled.OverFailureThreshold)
	}
	if len(got.Excluded) < 2 {
		t.Errorf("want both defective cells excluded and named, got %v", got.Excluded)
	}
}

// pastSaturation builds an open-loop cell the drift check flagged as having
// fallen behind its offered load, and for nothing else, with the minute-long
// TTFT a growing queue leaves behind.
func saturatedCell(policyName string, load bench.Load, repetition int, goodput float64) bench.Cell {
	c := cell(policyName, load, repetition, goodput)
	c.Backlog = 0.6
	// The failure check switched off, which is what a re-score or a fixture does:
	// a cell that only fell behind must not be counted as failing for it.
	c.FailureThreshold = -1
	c.TTFTP50Ns, c.TTFTP90Ns, c.TTFTP99Ns = (40 * time.Second).Nanoseconds(), (70 * time.Second).Nanoseconds(), (80 * time.Second).Nanoseconds()
	c.Flag("the fleet fell behind its offered load: 60% of the requests offered were still unanswered when arrivals stopped")
	return c
}

// Rule A of #33. Past its knee an open-loop cell is offered more than the fleet
// can serve, so it never reaches a steady state and the drift check now flags it
// — two-sided, it always will. Dropping it as a broken measurement would blank
// the table exactly where the cache-blind policies collapse, and a knee read off
// a gap is no knee. So the cell keeps its goodput, marked and named the way a
// failing cell is, and gives up only its latency percentiles, which are a moment
// in a queue that was still growing.
func TestACellPastSaturationKeepsItsGoodputButNotItsLatency(t *testing.T) {
	at16 := bench.OpenLoopAt(16)
	var cs []bench.Cell
	cs = append(cs,
		saturatedCell(policy.RoundRobinName, at16, 1, 0.0),
		saturatedCell(policy.RoundRobinName, at16, 2, 0.1),
		saturatedCell(policy.RoundRobinName, at16, 3, 0.0))
	steady := cell(policy.SessionAffinityName, at16, 1, 12.0)
	steady.TTFTP50Ns = (300 * time.Millisecond).Nanoseconds()
	cs = append(cs, steady,
		saturatedCell(policy.SessionAffinityName, at16, 2, 6.0),
		saturatedCell(policy.SessionAffinityName, at16, 3, 5.0))

	got := compare(t, cs)

	if len(got.Excluded) != 0 {
		t.Errorf("cells past saturation were excluded: %v", got.Excluded)
	}
	row := got.Rows[0]
	rr := row.Goodput[policy.RoundRobinName]
	if rr.Repetitions != 3 || rr.Saturated != 3 || rr.OverFailureThreshold != 0 || rr.MedianRPS != 0.0 {
		t.Errorf("round-robin pooled %d repetitions (%d past saturation, %d failing) at a median of %v, want 3, 3, 0 and 0",
			rr.Repetitions, rr.Saturated, rr.OverFailureThreshold, rr.MedianRPS)
	}
	if _, ok := row.Latency[policy.RoundRobinName]; ok {
		t.Errorf("round-robin has latency percentiles, but every one of its cells was past saturation: %+v", row.Latency[policy.RoundRobinName])
	}
	sa := row.Latency[policy.SessionAffinityName]
	if sa.Repetitions != 1 || time.Duration(sa.P50Ns) != 300*time.Millisecond {
		t.Errorf("session affinity's latency pooled %d repetitions at p50 %v, want only the steady one at 300ms",
			sa.Repetitions, time.Duration(sa.P50Ns))
	}

	report := got.Report()
	for _, want := range []string{
		"6.00 (5.00–12.00, n=3) ⚠",
		"`round_robin-a16-r2`: the fleet fell behind its offered load",
		"| open-loop | 16 req/s | round_robin | — | — | — |",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not carry %q:\n%s", want, report)
		}
	}
}

// At saturation a fleet also drops and times out, so a cell can be past its
// knee and past the failure threshold at once. Both say the same thing — a fleet
// falling over — and neither is a broken measurement, so the pair surfaces the
// cell as either would alone. A third reason of the other kind still wins.
func TestACellPastSaturationThatAlsoFailedIsSurfacedUnlessBrokenToo(t *testing.T) {
	at20 := bench.OpenLoopAt(20)
	both := pastFailureThreshold(saturatedCell(policy.RoundRobinName, at20, 1, 0.5))
	broken := saturatedCell(policy.RoundRobinName, at20, 2, 0.7)
	broken.Flag("the router ejected a replica 1 time(s) during this cell")

	var cs []bench.Cell
	cs = append(cs, both, broken)
	cs = append(cs, cells(policy.SessionAffinityName, at20, 10.0)...)

	got := compare(t, cs)

	rr := got.Rows[0].Goodput[policy.RoundRobinName]
	if rr.Repetitions != 1 || rr.Saturated != 1 || rr.OverFailureThreshold != 1 {
		t.Errorf("round-robin pooled %+v, want only the cell that was saturated and failing, counted as both", rr)
	}
	if len(got.Excluded) != 2 || !strings.Contains(strings.Join(got.Excluded, " "), "ejected a replica") {
		t.Errorf("excluded %v, want the cell that also lost a replica, under both its reasons", got.Excluded)
	}
	if len(got.Surfaced) != 2 {
		t.Errorf("surfaced %v, want the kept cell named under both of its reasons", got.Surfaced)
	}
}

// TestCellsRecordedUnderAnUnknownPolicyStillAppear. A cell's policy is the label
// the sweep was told to record, not a name it resolves, so a mistyped -policy
// produces cells under a name no policy has. Dropping them would hide a sweep.
func TestCellsRecordedUnderAnUnknownPolicyStillAppear(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0)...)
	cs = append(cs, cells("least_outstandng", at8, 9.0)...) // as typed

	got := compare(t, cs)

	if len(got.Policies) != 2 || got.Policies[0] != policy.RoundRobinName {
		t.Fatalf("policies = %v, want the baseline first and the unknown label after it", got.Policies)
	}
	if !strings.Contains(got.Report(), "least_outstandng") {
		t.Errorf("a sweep recorded under an unknown policy label is missing from the table:\n%s", got.Report())
	}
}

// TestALoadPointOnePolicyNeverReachedIsAGapNotAZero — the lesson of #10: a
// missing figure is not a spread of zero, and a policy that did not run at a load
// point did not score nothing there.
func TestALoadPointOnePolicyNeverReachedIsAGapNotAZero(t *testing.T) {
	at8, at256 := bench.ClosedLoopAt(8), bench.ClosedLoopAt(256)
	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0)...)
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 9.0)...)
	// The sweep was interrupted before least-outstanding reached the top rung.
	cs = append(cs, cells(policy.RoundRobinName, at256, 4.0)...)

	got := compare(t, cs)

	top := got.Rows[len(got.Rows)-1]
	if _, ok := top.Goodput[policy.LeastOutstandingName]; ok {
		t.Errorf("a policy that never ran at 256 users has a figure there: %+v", top)
	}
	report := got.Report()
	if !strings.Contains(report, "| closed-loop | 256 users | 4.00 (n=1) | — | — |") {
		t.Errorf("the unrun load point is not rendered as a gap:\n%s", report)
	}
}

// TestBothAxesReadSideBySideButNeverInterleaved. A concurrency and an arrival rate
// are not points on one scale, so the two axes are two blocks of rows.
func TestBothAxesReadSideBySideButNeverInterleaved(t *testing.T) {
	var cs []bench.Cell
	for _, load := range []bench.Load{bench.OpenLoopAt(16), bench.ClosedLoopAt(8)} {
		cs = append(cs, cells(policy.RoundRobinName, load, 8.0)...)
		cs = append(cs, cells(policy.LeastOutstandingName, load, 9.0)...)
	}

	got := compare(t, cs)

	if len(got.Rows) != 2 {
		t.Fatalf("%d rows, want one per load point", len(got.Rows))
	}
	if got.Rows[0].Load.Driver != bench.ClosedLoopDriver {
		t.Errorf("first row is %v, want the closed-loop axis first", got.Rows[0].Load.Driver)
	}
	if got.Rows[1].Load.Driver != bench.OpenLoopDriver {
		t.Errorf("second row is %v, want the open-loop axis after it", got.Rows[1].Load.Driver)
	}
}

// TestLoadCellsReadsWhatASweepWrote: the comparison is built from the records the
// runs left behind, so an interrupted sweep's completed cells still compare.
func TestLoadCellsReadsWhatASweepWrote(t *testing.T) {
	dir := t.TempDir()
	cellDir := filepath.Join(dir, "cells")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := cells(policy.RoundRobinName, bench.ClosedLoopAt(8), 8.0, 8.2)
	for _, c := range want {
		contents, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cellDir, c.ID+".json"), contents, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got, err := bench.LoadCells(dir)
	if err != nil {
		t.Fatalf("load cells: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d cells, want %d", len(got), len(want))
	}
	if got[0].GoodputRPS != 8.0 || got[0].Policy != policy.RoundRobinName {
		t.Errorf("cell = %+v, want the one that was written", got[0])
	}
}

func TestLoadCellsRefusesADirectoryWithNoCells(t *testing.T) {
	if _, err := bench.LoadCells(t.TempDir()); err == nil {
		t.Fatal("loading a directory with no cells returned no error")
	}
}

// TestATruncatedCellRecordIsAnErrorWhenReporting. Resuming a sweep treats an
// unreadable record as a cell to re-run; reporting cannot, because a cell dropped
// here is a row missing from a published table.
func TestATruncatedCellRecordIsAnErrorWhenReporting(t *testing.T) {
	dir := t.TempDir()
	cellDir := filepath.Join(dir, "cells")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "round_robin-c8-r1.json"), []byte(`{"id": "round`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := bench.LoadCells(dir)
	if err == nil {
		t.Fatal("a truncated cell record was read without complaint")
	}
	if !strings.Contains(err.Error(), "not a cell record") {
		t.Errorf("err = %v, want it to name the unreadable record", err)
	}
}

// A guard on the example in the docs: the id a cell is written under is the id the
// comparison reads back.
func TestCellIDsAreStableAcrossTheComparison(t *testing.T) {
	got := bench.CellID(policy.LeastOutstandingName, bench.ClosedLoopAt(8), 2)
	if want := fmt.Sprintf("%s-c8-r2", policy.LeastOutstandingName); got != want {
		t.Errorf("cell id = %q, want %q", got, want)
	}
}

// TestAComparisonWithNothingLeftInItSaysSo. A table of em dashes has two very
// different explanations — the sweep did not run, or every cell was thrown away —
// and the reader should not have to work out which.
func TestAComparisonWithNothingLeftInItSaysSo(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	var cs []bench.Cell
	for _, name := range []string{policy.RoundRobinName, policy.LeastOutstandingName} {
		dirty := cell(name, at8, 1, 8.0)
		dirty.Clean, dirty.Flagged = false, true
		dirty.FlagReasons = []string{"a foreign process held 3,214 MiB on GPU 2"}
		cs = append(cs, dirty)
	}

	report := compare(t, cs).Report()

	if !strings.Contains(report, "Nothing in this table rests on a usable cell") {
		t.Errorf("a comparison with every cell excluded does not say so:\n%s", report)
	}
}

// TestOneCellReadTwiceIsOneRepetitionNotTwo. A cell id is a cell's identity, and
// two directories can hold the same record — the same directory named twice, a
// copy of a run kept beside it, or a sweep that wrote both axes into one place and
// is then read alongside one of them. Counting it twice would report a spread of
// zero across "two" repetitions, which is a claim about reproducibility that one
// measurement cannot make, and would let the delta call itself replicated.
func TestOneCellReadTwiceIsOneRepetitionNotTwo(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	one := cells(policy.RoundRobinName, at8, 8.0)
	var cs []bench.Cell
	cs = append(cs, one...)
	cs = append(cs, one...)
	cs = append(cs, cells(policy.LeastOutstandingName, at8, 9.0)...)

	got := compare(t, cs)

	pooled := got.Rows[0].Goodput[policy.RoundRobinName]
	if pooled.Repetitions != 1 {
		t.Errorf("one record read twice pooled as %d repetitions, want 1", pooled.Repetitions)
	}
	if strings.Contains(got.Report(), "8.00–8.00") {
		t.Errorf("the report claims a spread across one measurement:\n%s", got.Report())
	}
}

// TestTheSameExcludedCellIsNotListedTwice: the footnote is evidence, and evidence
// repeated reads as two contaminated cells where there was one.
func TestTheSameExcludedCellIsNotListedTwice(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	dirty := cell(policy.RoundRobinName, at8, 1, 8.0)
	dirty.Flagged = true
	dirty.FlagReasons = []string{"a foreign process held 3,214 MiB on GPU 2"}

	got := compare(t, []bench.Cell{dirty, dirty, cell(policy.LeastOutstandingName, at8, 1, 9.0)})

	if n := strings.Count(got.Report(), "foreign process held 3,214 MiB"); n != 1 {
		t.Errorf("the excluded cell is listed %d times, want once:\n%s", n, got.Report())
	}
}

// TestTwoDifferentMeasurementsUnderOneCellIdAreRefused. This is the shape a stale
// sweep directory arrives in: ADR-0004 partitioned the workload's user space by
// axis, so a cell recorded before that sent different bytes under the same id, and
// the ADR says such a directory must be deleted rather than read. Picking one of
// them silently would publish an arbitrary choice between two measurements.
func TestTwoDifferentMeasurementsUnderOneCellIdAreRefused(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	first := cell(policy.RoundRobinName, at8, 1, 8.0)
	first.StartedAtNs = 1_000
	stale := first
	stale.StartedAtNs = 2_000
	stale.GoodputRPS = 25.0

	_, err := bench.Compare([]bench.Cell{first, stale, cell(policy.LeastOutstandingName, at8, 1, 9.0)})
	if err == nil {
		t.Fatal("two different measurements under one cell id were merged without complaint")
	}
	if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("err = %v, want it to name the cell id in conflict", err)
	}
}

// TestWhenTwoCopiesOfOneCellDisagreeAboutBeingUsableTheStricterStands. Two records
// of one measurement can differ in their verdict without differing in their figures
// — one resummarised against a threshold the other was not. Deciding which to trust
// by the order the directories were named would be a coin flip over a published
// median.
func TestWhenTwoCopiesOfOneCellDisagreeAboutBeingUsableTheStricterStands(t *testing.T) {
	at8 := bench.ClosedLoopAt(8)
	pooled := cell(policy.RoundRobinName, at8, 1, 8.0)
	flagged := pooled
	flagged.Flagged = true
	flagged.FlagReasons = []string{"failure rate 12.00% exceeds the 1.00% threshold"}
	other := cells(policy.LeastOutstandingName, at8, 9.0)

	// Whichever order the two copies arrive in, the cell is excluded.
	for _, order := range [][]bench.Cell{{pooled, flagged}, {flagged, pooled}} {
		got := compare(t, append(order, other...))

		if _, ok := got.Rows[0].Goodput[policy.RoundRobinName]; ok {
			t.Errorf("a cell one copy calls broken was pooled anyway")
		}
		if !strings.Contains(got.Report(), "failure rate 12.00%") {
			t.Errorf("the exclusion is not reported:\n%s", got.Report())
		}
	}
}
