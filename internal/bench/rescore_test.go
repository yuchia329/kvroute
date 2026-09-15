package bench_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/yuchia329/kvroute/internal/bench"
)

// Re-scoring a recorded cell from its recorded rows, which is how a change to
// the summary's arithmetic is held against the 216 open-loop cells already
// measured rather than only against the next run.

// recordedCell writes a sweep directory holding one cell: the record as it was
// written at the time, and the rows it was written from.
//
// The recorded summary deliberately carries the verdict the old check reached,
// so the test is about the two being put side by side rather than about the
// summary agreeing with itself.
func recordedCell(t *testing.T, workload string, rows []bench.Result, recorded bench.Summary) string {
	t.Helper()
	dir := t.TempDir()
	cells := filepath.Join(dir, "cells")
	if err := os.MkdirAll(cells, 0o755); err != nil {
		t.Fatal(err)
	}

	cell := map[string]any{
		"id":           "prefix_affinity-a8-r1",
		"policy":       "prefix_affinity",
		"driver":       "open_loop",
		"arrival_rate": 8,
		"repetition":   1,
		"workload":     workload,
		"summary":      recorded,
	}
	encoded, err := json.Marshal(cell)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cells, "prefix_affinity-a8-r1.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	var lines strings.Builder
	for _, r := range rows {
		// The compacted form is one file for the whole sweep, so a row's cell
		// identity has to be on the row or it cannot be grouped back.
		r.CellID = "prefix_affinity-a8-r1"
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(cells, "prefix_affinity-a8-r1.jsonl"), []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// degradingRows are a cell that got three times slower across its window: the
// shape the old one-sided check published unflagged.
func degradingRows() []bench.Result {
	start := time.Unix(1757000000, 0)
	var rows []bench.Result
	for i := range 40 {
		ttft := time.Second
		if i >= 20 {
			ttft = 3 * time.Second
		}
		rows = append(rows, arrival(start.Add(time.Duration(i)*time.Second), i%4, ttft, time.Second))
	}
	return rows
}

func TestARecordedCellIsPutBesideWhatTheCurrentCheckMakesOfItsRows(t *testing.T) {
	rows := degradingRows()
	// What the old check recorded: a large negative drift, tested one-sided, so
	// no flag at all.
	dir := recordedCell(t, "multiturn(sessions=922,ws=3,skew=1,turns=4)", rows, bench.Summary{
		Requests: 40, Successes: 40, WarmupDrift: -0.667,
		WindowNs: (40 * time.Second).Nanoseconds(),
		// Worked from the rows: twenty successes at 1s and twenty at 3s, so the
		// median of the forty is the twentieth, which is 1s.
		TTFTP50Ns: time.Second.Nanoseconds(), TTFTP99Ns: (3 * time.Second).Nanoseconds(),
		FailureThreshold: -1, ScheduleLagThresholdNs: -1,
	})

	scored, err := bench.Rescore([]string{dir}, bench.DefaultWarmupDriftThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if len(scored) != 1 {
		t.Fatalf("re-scored %d cells, want 1", len(scored))
	}
	got := scored[0]

	if got.Was.WarmupDriftFlagged() {
		t.Errorf("the recorded summary is read as flagged: %v", got.Was.FlagReasons)
	}
	if !got.Changed() {
		t.Error("a cell that was unflagged and now flags is not reported as changed")
	}
	if want := bench.WarmupDriftDegrading; got.Now.WarmupDriftVerdict() != string(want) {
		t.Errorf("current verdict is %q, want %q", got.Now.WarmupDriftVerdict(), want)
	}
	// The re-score's own audit: everything the drift change does not touch has
	// to come back identical, or the rows are not the rows the record was
	// written from and no verdict read off them means anything.
	if got.Now.Requests != got.Was.Requests || got.Now.TTFTP50Ns != got.Was.TTFTP50Ns || got.Now.WindowNs != got.Was.WindowNs {
		t.Errorf("the re-score did not reproduce the recorded summary: %d/%d requests, TTFT p50 %v/%v, window %v/%v",
			got.Now.Requests, got.Was.Requests,
			time.Duration(got.Now.TTFTP50Ns), time.Duration(got.Was.TTFTP50Ns),
			time.Duration(got.Now.WindowNs), time.Duration(got.Was.WindowNs))
	}

	report := bench.RescoreReport(scored, bench.DefaultWarmupDriftThreshold, false)
	for _, want := range []string{"1 of 1 cells changed verdict", "reproduced its recorded", string(bench.WarmupDriftDegrading)} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
}

// Writing the re-score back is what lets every table and figure downstream of a
// cell record — the comparison, the figures, the map — judge it by the current
// check without each of them re-deriving summaries from rows. The recorded
// verdict is not lost by it: the re-score report published beside the change is
// the record of what moved, and git holds the old files.
//
// What it must not do is lose the evidence the rows cannot supply. A replica
// ejected mid-cell is known only from the record, so the flag it raised is
// reapplied, exactly as a sweep reapplies it when it resummarises a cached cell.
func TestWritingTheRescoreBackUpdatesTheRecordAndKeepsItsOwnEvidence(t *testing.T) {
	// The record as the run wrote it: the summary its rows produce with no drift
	// check to stop it, on a clean fleet that lost a replica mid-cell.
	rows := degradingRows()
	summary := bench.Summarize(rows, bench.SummaryOptions{WarmupDriftThreshold: -1})
	summary.Flag("the router ejected a replica 1 time(s) during this cell, so it measured a smaller fleet for part of its window; " +
		"nothing was dropped to show it, because the router reroutes around a missing replica. Re-run it")
	// A column added after this cell ran. The rows could fill it in, but writing
	// the drift verdict back is not the place to publish a figure nobody asked
	// for, so it has to stay as it was recorded.
	summary.TTFTP90Ns = 0
	dir := recordedCell(t, "multiturn(sessions=922,ws=3,skew=1,turns=4)", rows, summary)
	path := filepath.Join(dir, "cells", "prefix_affinity-a8-r1.json")
	var recorded map[string]any
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &recorded); err != nil {
		t.Fatal(err)
	}
	recorded["ejections"] = 1
	recorded["contamination"] = map[string]any{"gpu_samples": 12, "clean": true}
	if contents, err = json.Marshal(recorded); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bench.Compact(dir); err != nil {
		t.Fatal(err)
	}

	scored, err := bench.Rescore([]string{dir}, bench.DefaultWarmupDriftThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if err := bench.WriteRescored(scored); err != nil {
		t.Fatal(err)
	}

	reloaded, err := bench.LoadCells(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(reloaded[0].FlagReasons, " | ")
	if reloaded[0].WarmupDriftVerdict() != string(bench.WarmupDriftDegrading) {
		t.Errorf("the written record's verdict is %q, want %q: %s", reloaded[0].WarmupDriftVerdict(), bench.WarmupDriftDegrading, got)
	}
	if !strings.Contains(got, "ejected a replica") {
		t.Errorf("writing the re-score back dropped the flag only the record could supply: %s", got)
	}
	if reloaded[0].TTFTP90Ns != 0 {
		t.Errorf("writing the drift verdict back also filled in TTFT p90 (%v), a column the record never had", time.Duration(reloaded[0].TTFTP90Ns))
	}

	// The compacted copy of the records is rebuilt from them, so the two forms a
	// sweep directory keeps cannot disagree about a verdict.
	compacted, err := parquet.ReadFile[bench.Cell](filepath.Join(dir, bench.CellsParquet))
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 1 || compacted[0].WarmupDriftVerdict() != string(bench.WarmupDriftDegrading) {
		t.Errorf("cells.parquet still holds the old verdict: %+v", compacted)
	}
}

// A cell whose rows do not reproduce its record is not written back: a verdict
// read off the wrong rows would overwrite a record with a summary of something
// else.
func TestACellWhoseRowsDoNotReproduceItIsNotWrittenBack(t *testing.T) {
	dir := recordedCell(t, "multiturn(sessions=922,ws=3,skew=1,turns=4)", degradingRows(), bench.Summary{
		Requests: 99, Successes: 99, FailureThreshold: -1, ScheduleLagThresholdNs: -1,
	})
	path := filepath.Join(dir, "cells", "prefix_affinity-a8-r1.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	scored, err := bench.Rescore([]string{dir}, bench.DefaultWarmupDriftThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if err := bench.WriteRescored(scored); err == nil || !strings.Contains(err.Error(), "prefix_affinity-a8-r1") {
		t.Errorf("writing back a cell whose rows disagree with its record returned %v, want an error naming it", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the record was overwritten although its rows do not reproduce it")
	}
}

// A published measurement keeps its rows compacted rather than as the JSONL the
// run wrote (ADR-0002), so a re-score that could only read the live form could
// not re-score anything already published — which is the entire point of it.
func TestRowsAreReadBackFromTheCompactedFormToo(t *testing.T) {
	dir := recordedCell(t, "multiturn(sessions=922,ws=3,skew=1,turns=4)", degradingRows(), bench.Summary{Requests: 40})
	if _, err := bench.Compact(dir); err != nil {
		t.Fatal(err)
	}
	// Exactly what publishing does: the compacted file is kept and the per-cell
	// JSONL is left on the box.
	if err := os.Remove(filepath.Join(dir, "cells", "prefix_affinity-a8-r1.jsonl")); err != nil {
		t.Fatal(err)
	}

	rows, err := bench.LoadCellRows(dir)
	if err != nil {
		t.Fatal(err)
	}
	of := rows["prefix_affinity-a8-r1"]
	if len(of) != 40 {
		t.Fatalf("read %d rows back from the compacted form, want 40", len(of))
	}
	// The two columns the check depends on, which a schema change could drop
	// silently: the turn index it stratifies by and the due time it splits on.
	var turns int
	for _, r := range of {
		if r.ScheduledAtNs == 0 {
			t.Fatal("a row came back from the compacted form with no due time, so the split would fall back to when it was sent")
		}
		turns |= 1 << r.Turn
	}
	if turns != 0b1111 {
		t.Errorf("turn indices %04b came back, want all four: the stratifier does not survive compaction", turns)
	}
}

// A directory with cell records and no rows is an error rather than a cell
// silently scored against nothing.
func TestADirectoryWithNoRowsCannotBeRescored(t *testing.T) {
	dir := recordedCell(t, "multiturn(sessions=922,ws=3,skew=1,turns=4)", degradingRows(), bench.Summary{Requests: 40})
	if err := os.Remove(filepath.Join(dir, "cells", "prefix_affinity-a8-r1.jsonl")); err != nil {
		t.Fatal(err)
	}
	_, err := bench.Rescore([]string{dir}, bench.DefaultWarmupDriftThreshold)
	if err == nil {
		t.Fatal("a directory holding no rows re-scored without complaint")
	}
	if !strings.Contains(err.Error(), "cannot be re-scored") {
		t.Errorf("the error does not say the cells have no rows: %v", err)
	}
}

// The three causes are read back out of the flag text because the 216 recorded
// cells predate any field that could have held them. A message that merely
// mentions warming up must not be read as one that flagged for it.
func TestACauseIsReadFromTheFlagItOpensWith(t *testing.T) {
	if causes := (bench.Summary{FlagReasons: []string{
		"3 requests were cancelled while the fleet was still warming up: the cell did not run to completion",
	}}).WarmupDriftCauses(); len(causes) != 0 {
		t.Errorf("a flag that is not the drift check's was read as one: %v", causes)
	}
	if got := (bench.Summary{}).WarmupDriftVerdict(); got != "clear" {
		t.Errorf("an unflagged summary reads as %q, want \"clear\"", got)
	}
}

// A cell whose rows do not reproduce its record is called out rather than
// reported as a verdict change, because a verdict read off the wrong rows says
// nothing.
func TestRowsThatDoNotReproduceTheRecordAreCalledOut(t *testing.T) {
	dir := recordedCell(t, "multiturn(sessions=922,ws=3,skew=1,turns=4)", degradingRows(), bench.Summary{
		Requests: 99, Successes: 99, FailureThreshold: -1, ScheduleLagThresholdNs: -1,
	})
	scored, err := bench.Rescore([]string{dir}, bench.DefaultWarmupDriftThreshold)
	if err != nil {
		t.Fatal(err)
	}
	report := bench.RescoreReport(scored, bench.DefaultWarmupDriftThreshold, false)
	if !strings.Contains(report, "did not reproduce their recorded summary") {
		t.Errorf("the report does not name the cell whose rows disagree with its record:\n%s", report)
	}
}

// Guarding the one input the check cannot infer: a workload that states no turns
// per session is one whose turn index is a counter, and the check has to pool
// rather than stratify.
func TestAWorkloadStatingNoTurnsPerSessionIsScoredPooled(t *testing.T) {
	dir := recordedCell(t, "fixed(prompt=2048B,output=64t,seed=1)", degradingRows(), bench.Summary{
		Requests: 40, FailureThreshold: -1, ScheduleLagThresholdNs: -1,
	})
	scored, err := bench.Rescore([]string{dir}, bench.DefaultWarmupDriftThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if got := scored[0].Now.WarmupDriftBasis; got != "pooled" {
		t.Errorf("drift basis is %q, want \"pooled\": the fixed workload never returns to a turn index", got)
	}
}
