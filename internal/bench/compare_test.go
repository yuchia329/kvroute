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
	dirty.FlagReasons = []string{"failure rate 12.00% exceeds the 1.00% threshold"}

	var cs []bench.Cell
	cs = append(cs, cells(policy.RoundRobinName, at8, 8.0, 8.0, 8.0)...)
	cs = append(cs, cell(policy.LeastOutstandingName, at8, 1, 9.0), dirty)

	got := compare(t, cs)

	pooled := got.Rows[0].Goodput[policy.LeastOutstandingName]
	if pooled.Repetitions != 1 || pooled.MaxRPS == 99.0 {
		t.Errorf("the flagged cell was averaged in: pooled %d repetitions, max %v", pooled.Repetitions, pooled.MaxRPS)
	}
	report := got.Report()
	if !strings.Contains(report, "failure rate 12.00%") {
		t.Errorf("the excluded cell's reason is not reported:\n%s", report)
	}
	if !strings.Contains(report, "(n=1)") {
		t.Errorf("a figure resting on one repetition does not say so:\n%s", report)
	}
}

// TestAnUncleanCellIsExcludedWhetherOrNotItWasFlagged. Contamination keeps Clean
// as its own field so that "nothing was found" and "nothing was looked for" cannot
// be collapsed; a comparison that inferred cleanliness from the flags would pool a
// record where the two disagree.
func TestAnUncleanCellIsExcludedWhetherOrNotItWasFlagged(t *testing.T) {
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
	if !strings.Contains(got.Report(), "not clean") {
		t.Errorf("the report does not say the cell was excluded for being unclean:\n%s", got.Report())
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
