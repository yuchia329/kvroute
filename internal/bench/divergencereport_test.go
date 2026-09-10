package bench_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
)

// sweepOnDisk writes cells and their rows the way a sweep leaves them, so the
// divergence report is exercised through the files it actually reads rather than
// through an in-memory shortcut that no run produces.
func sweepOnDisk(t *testing.T, cells ...cellRows) string {
	t.Helper()
	dir := t.TempDir()
	cellDir := filepath.Join(dir, "cells")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("create cell dir: %v", err)
	}
	for _, written := range cells {
		cell, rows := written.cell, written.rows
		contents, err := json.MarshalIndent(cell, "", "  ")
		if err != nil {
			t.Fatalf("marshal cell: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cellDir, cell.ID+".json"), append(contents, '\n'), 0o644); err != nil {
			t.Fatalf("write cell: %v", err)
		}
		var b strings.Builder
		w := record.NewWriter[bench.Result](&b)
		for _, row := range rows {
			if err := w.Write(row); err != nil {
				t.Fatalf("write row: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close rows: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cellDir, cell.ID+".jsonl"), []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write rows: %v", err)
		}
	}
	return dir
}

// cellRows is one cell and the rows it produced. A slice of pairs rather than a
// map keyed by the cell, because a Cell carries its flag reasons and so is not
// comparable.
type cellRows struct {
	cell bench.Cell
	rows []bench.Result
}

// row builds a measured, successful row that carries both sides of the
// divergence. Four bytes a token throughout, so the prediction converts cleanly.
func row(session string, startedMs int64, totalMs int64, matchBytes int, promptTokens, cachedTokens int) bench.Result {
	return bench.Result{
		Labels:             bench.Labels{CellID: "cell", Policy: "prefix_affinity"},
		RequestID:          session + "-" + string(rune('a'+startedMs%26)),
		Session:            session,
		StartedAtNs:        startedMs * 1e6,
		TotalNs:            totalMs * 1e6,
		PromptBytes:        int64(promptTokens * 4),
		PrefixMatchBytes:   matchBytes,
		EnginePromptTokens: promptTokens,
		EngineCachedTokens: cachedTokens,
		EngineUsageRead:    true,
		EngineCacheRead:    true,
		Outcome:            record.OutcomeSuccess,
	}
}

// AC3: the plot distinguishes the two directions, so the report cannot net them.
func TestTheReportSplitsOverPredictionFromUnderPrediction(t *testing.T) {
	cell := bench.Cell{ID: "cell", Policy: "prefix_affinity", WorkingSet: 1, PrefixIndexNodes: 100, PrefixIndexCap: 100}
	dir := sweepOnDisk(t, cellRows{cell, []bench.Result{
		// Believed 400 tokens, the engine held 100: over by 300.
		row("s1", 0, 10, 1600, 1000, 100),
		// Believed 100 tokens, the engine held 400: under by 300.
		row("s2", 20, 10, 400, 1000, 400),
	}})

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}
	if report.Overall.OverTokens != 300 || report.Overall.UnderTokens != 300 {
		t.Errorf("over/under tokens = %v/%v, want 300/300",
			report.Overall.OverTokens, report.Overall.UnderTokens)
	}
	if report.Overall.Over != 1 || report.Overall.Under != 1 {
		t.Errorf("over/under requests = %d/%d, want 1/1", report.Overall.Over, report.Overall.Under)
	}

	rendered := report.Report()
	if !strings.Contains(rendered, "over-predicted") || !strings.Contains(rendered, "under-predicted") {
		t.Errorf("the report does not name both directions:\n%s", rendered)
	}
}

// AC2's second axis: how stale the belief was when it was acted on. It is
// derived from the rows rather than recorded by the router, so a request's
// recency is the gap between its own start and the end of that session's
// previous turn.
func TestDivergenceIsBinnedByTimeSinceTheSessionWasLastServed(t *testing.T) {
	cell := bench.Cell{ID: "cell", Policy: "prefix_affinity", WorkingSet: 1}
	dir := sweepOnDisk(t, cellRows{cell, []bench.Result{
		// First turn: nothing served this session before it.
		row("s1", 0, 100, 400, 1000, 100),
		// Second turn starts 500ms after the first finished: under a second.
		row("s1", 600, 100, 400, 1000, 100),
		// Third turn starts 6.3s after the second finished.
		row("s1", 7000, 100, 400, 1000, 100),
	}})

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}

	byLabel := map[string]int{}
	for _, bin := range report.ByRecency {
		byLabel[bin.Label] = bin.Requests
	}
	if byLabel[bench.FirstTurnLabel] != 1 {
		t.Errorf("first-turn requests = %d, want 1: %v", byLabel[bench.FirstTurnLabel], byLabel)
	}
	if total := byLabel[bench.FirstTurnLabel] + byLabel["<1s"] + byLabel["5s-10s"]; total != 3 {
		t.Errorf("the three turns did not land in the expected buckets: %v", byLabel)
	}
}

// A row that never got an engine account of its prompt cannot contribute a
// divergence, and counting it as one that diverged by nothing would report an
// index as accurate on requests nobody checked.
func TestRowsWithNoEngineAccountAreCountedAsUnmeasuredRatherThanAccurate(t *testing.T) {
	cell := bench.Cell{ID: "cell", Policy: "prefix_affinity", WorkingSet: 1}
	measured := row("s1", 0, 10, 400, 1000, 100)
	silent := row("s2", 20, 10, 400, 1000, 100)
	silent.EngineUsageRead, silent.EngineCacheRead = false, false

	dir := sweepOnDisk(t, cellRows{cell, []bench.Result{measured, silent}})

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}
	if report.Overall.Requests != 1 {
		t.Errorf("divergence requests = %d, want 1", report.Overall.Requests)
	}
	if report.Rows != 2 || report.Unaccounted != 1 {
		t.Errorf("rows/unaccounted = %d/%d, want 2/1", report.Rows, report.Unaccounted)
	}
	if !strings.Contains(report.Report(), "1 of 2") {
		t.Errorf("the report does not say how much of the run it could measure:\n%s", report.Report())
	}
}

// Warm-up rows are excluded from the divergence, as they are from every other
// summary — but they still establish when a session was last served, or the turn
// after a warm-up turn would be reported as a session's first.
func TestWarmupRowsAreExcludedButStillDateTheSession(t *testing.T) {
	cell := bench.Cell{ID: "cell", Policy: "prefix_affinity", WorkingSet: 1}
	warm := row("s1", 0, 100, 400, 1000, 100)
	warm.Warmup = true
	dir := sweepOnDisk(t, cellRows{cell, []bench.Result{
		warm,
		row("s1", 600, 100, 400, 1000, 100),
	}})

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}
	if report.Overall.Requests != 1 {
		t.Errorf("divergence requests = %d, want 1: the warm-up row was measured", report.Overall.Requests)
	}
	for _, bin := range report.ByRecency {
		if bin.Label == bench.FirstTurnLabel && bin.Requests > 0 {
			t.Error("the measured turn is reported as its session's first: the warm-up turn before it did not date the session")
		}
	}
}

// The report's calibration reading is the one the node cap is resized against,
// so it has to come from the policy that actually consulted an index. A round
// robin cell predicts zero on every request; folding it in would dilute the
// honoured share with requests that never claimed anything.
func TestOnlyThePoliciesThatConsultedAnIndexCalibrateTheCap(t *testing.T) {
	predicting := bench.Cell{ID: "prefix_affinity-c8-r1", Policy: "prefix_affinity", WorkingSet: 1, PrefixIndexNodes: 100, PrefixIndexCap: 100}
	blind := bench.Cell{ID: "round_robin-c8-r1", Policy: "round_robin", WorkingSet: 1}

	predictingRow := row("s1", 0, 10, 1600, 1000, 200) // claimed 400, held 200
	blindRow := row("s2", 20, 10, 0, 1000, 200)        // claimed nothing
	blindRow.Labels.Policy = "round_robin"
	blindRow.Labels.CellID = blind.ID
	predictingRow.Labels.CellID = predicting.ID

	dir := sweepOnDisk(t,
		cellRows{predicting, []bench.Result{predictingRow}},
		cellRows{blind, []bench.Result{blindRow}},
	)

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}
	if report.Overall.Requests != 2 {
		t.Errorf("overall requests = %d, want 2 — the report covers every policy", report.Overall.Requests)
	}

	calibration := report.ForCalibration()
	if calibration.Requests != 1 {
		t.Fatalf("calibration requests = %d, want 1 — only the policy that consulted an index", calibration.Requests)
	}
	honoured, ok := calibration.Honoured()
	if !ok || honoured != 0.5 {
		t.Errorf("honoured = %v (%v), want 0.5", honoured, ok)
	}
	if !calibration.CapBound() {
		t.Error("the calibration lost the index occupancy, so nothing can say whether the cap bound")
	}
}

// A sweep must not resume into a directory whose cells sent different bytes.
//
// Asking the engine for usage changed every request body, so the workload names
// changed with it — which is ADR-0004's rule working. What that rule cannot do on
// its own is stop the resume: a cached cell is kept on its id alone, so a
// directory swept before the change and resumed after it would end up holding two
// workloads' cells, and `compare` would refuse the table only once the GPU time
// had been spent.
func TestASweepRefusesToResumeIntoCellsThatSentDifferentBytes(t *testing.T) {
	stale := bench.Cell{
		ID:       "prefix_affinity-c4-r1",
		Policy:   "prefix_affinity",
		Workload: "multiturn(sessions=40,skew=0,turns=4,seed=1)",
	}
	dir := sweepOnDisk(t, cellRows{stale, []bench.Result{row("s1", 0, 10, 400, 1000, 100)}})

	_, err := bench.RunSweep(t.Context(), bench.SweepConfig{
		Dir:           dir,
		Target:        "http://127.0.0.1:1",
		Policy:        "prefix_affinity",
		Concurrencies: []int{4},
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel}),
	})
	if err == nil {
		t.Fatal("the sweep resumed into a directory holding cells that sent different bytes")
	}
	if !strings.Contains(err.Error(), stale.Workload) {
		t.Errorf("the refusal does not name the workload already in the directory: %v", err)
	}
	if !strings.Contains(err.Error(), stale.ID) {
		t.Errorf("the refusal does not name a cell that carries it: %v", err)
	}
}

// A directory holding cells of the workload now offered resumes as it always has.
// The check has to be a guard against mixing, not a second reason a sweep will not
// start.
func TestASweepStillResumesIntoCellsOfTheSameWorkload(t *testing.T) {
	offered := bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel})
	cached := bench.Cell{ID: "round_robin-c4-r1", Policy: "round_robin", Workload: offered.Name()}
	dir := sweepOnDisk(t, cellRows{cached, []bench.Result{row("s1", 0, 10, 0, 1000, 100)}})

	cells, err := bench.RunSweep(t.Context(), bench.SweepConfig{
		Dir:           dir,
		Target:        "http://127.0.0.1:1",
		Policy:        "round_robin",
		Concurrencies: []int{4},
		Workload:      offered,
	})
	// The router is not there, so the sweep stops at its own router check — but
	// it has to get that far, which it cannot if the workload check refused first.
	if err != nil && strings.Contains(err.Error(), "workload") {
		t.Fatalf("a cell of the workload now offered was refused: %v", err)
	}
	_ = cells
}

// #16 sweeps policy 4 at four spill points per axis, one of which declines
// nothing. Divergence has to keep them apart: spill diverts exactly the requests
// that would have tested the index's best-match belief, so a blended reading
// measures the index on the wrong population.
func TestDivergenceIsBinnedBySpillPoint(t *testing.T) {
	off := bench.Cell{ID: "prefix_affinity-c64-r1", Policy: "prefix_affinity", WorkingSet: 3}
	kv := bench.Cell{ID: "prefix_affinity-c64-r2", Policy: "prefix_affinity", WorkingSet: 3, KVHighWater: 0.85}
	load := bench.Cell{ID: "prefix_affinity-c64-r3", Policy: "prefix_affinity", WorkingSet: 1, LoadImbalanceFactor: 4}

	offRow := row("s1", 0, 10, 1600, 1000, 200)
	offRow.Labels.CellID = off.ID
	kvRow := row("s2", 20, 10, 1600, 1000, 100)
	kvRow.Labels.CellID = kv.ID
	loadRow := row("s3", 40, 10, 1600, 1000, 100)
	loadRow.Labels.CellID = load.ID

	dir := sweepOnDisk(t,
		cellRows{off, []bench.Result{offRow}},
		cellRows{kv, []bench.Result{kvRow}},
		cellRows{load, []bench.Result{loadRow}},
	)

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}
	if len(report.BySpill) != 3 {
		t.Fatalf("spill bins = %d, want 3 (off, kv, load): %+v", len(report.BySpill), report.BySpill)
	}
	if report.BySpill[0].Label != bench.SpillOffLabel {
		t.Errorf("the spill-off reference does not lead the table: %q", report.BySpill[0].Label)
	}
	for _, bin := range report.BySpill {
		if bin.Requests != 1 {
			t.Errorf("spill bin %q holds %d requests, want 1", bin.Label, bin.Requests)
		}
	}
}

// The node cap is sized off the spill-off cells alone. A reading blended across
// thresholds would size the index against requests the spill rule sent somewhere
// the index never claimed anything about.
func TestOnlyTheSpillOffCellsCalibrateTheCap(t *testing.T) {
	off := bench.Cell{ID: "prefix_affinity-c64-r1", Policy: "prefix_affinity",
		WorkingSet: 3, PrefixIndexNodes: 100, PrefixIndexCap: 100}
	spilling := bench.Cell{ID: "prefix_affinity-c64-r2", Policy: "prefix_affinity",
		WorkingSet: 3, KVHighWater: 0.85, PrefixIndexNodes: 100, PrefixIndexCap: 100}

	// Spill-off: claimed 400 tokens, engine held 200 -> half honoured.
	offRow := row("s1", 0, 10, 1600, 1000, 200)
	offRow.Labels.CellID = off.ID
	// Spilling: claimed 400, engine held none. Folding it in would drag the
	// honoured share down to 25% and shrink the cap on the wrong evidence.
	spillRow := row("s2", 20, 10, 1600, 1000, 0)
	spillRow.Labels.CellID = spilling.ID

	dir := sweepOnDisk(t,
		cellRows{off, []bench.Result{offRow}},
		cellRows{spilling, []bench.Result{spillRow}},
	)

	report, err := bench.MeasureDivergence([]string{dir})
	if err != nil {
		t.Fatalf("measure divergence: %v", err)
	}
	calibration := report.ForCalibration()
	if calibration.Requests != 1 {
		t.Fatalf("calibration requests = %d, want 1 — the spill-off cell alone", calibration.Requests)
	}
	honoured, ok := calibration.Honoured()
	if !ok || honoured != 0.5 {
		t.Errorf("honoured = %v (%v), want 0.5 — the spilling cell was folded in", honoured, ok)
	}
}

// A grid cell's skew has to travel with it, or every working-set bin pools the
// whole skew axis. Skew discounts realised pressure hard, so a WS bin averaging
// three skew levels mixes a wide range of the pressure the plot is about.
func TestACellCarriesTheSkewItOffered(t *testing.T) {
	offered, err := bench.NewMultiTurn(bench.MultiTurnWorkload{
		Model: testModel, Sessions: 40, CapacityTokens: 629_760, Skew: 1.4,
	})
	if err != nil {
		t.Fatalf("new multi-turn: %v", err)
	}
	if got := bench.OfferedSkew(offered); got != 1.4 {
		t.Errorf("skew = %v, want 1.4", got)
	}
	// The sweep hands every cell a Shifted workload, as it does for the working
	// set, and a wrapper that dropped this would report the whole grid at skew 0.
	if got := bench.OfferedSkew(bench.Shifted(offered, bench.WorkloadStride)); got != 1.4 {
		t.Errorf("skew through Shifted = %v, want 1.4", got)
	}
}
