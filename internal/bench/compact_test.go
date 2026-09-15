package bench_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
	"github.com/yuchia329/kvroute/internal/record"
)

func TestCompactionRewritesTheRunAsParquetWithoutLosingRows(t *testing.T) {
	dir := t.TempDir()
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1, 2}})

	got, err := bench.Compact(dir)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}

	wantRows := 0
	for _, c := range cells {
		wantRows += c.Requests + c.Warmup
	}
	if got.Requests != wantRows {
		t.Errorf("compacted %d rows, want %d", got.Requests, wantRows)
	}
	if got.Cells != len(cells) {
		t.Errorf("compacted %d cells, want %d", got.Cells, len(cells))
	}

	rows, err := parquet.ReadFile[bench.Result](got.RequestsPath)
	if err != nil {
		t.Fatalf("read %s: %v", got.RequestsPath, err)
	}
	if len(rows) != wantRows {
		t.Fatalf("%s holds %d rows, want %d", got.RequestsPath, len(rows), wantRows)
	}
	if rows[0].CellID == "" || rows[0].Outcome == "" {
		t.Errorf("a compacted row lost its labels: %+v", rows[0])
	}

	compacted, err := parquet.ReadFile[bench.Cell](got.CellsPath)
	if err != nil {
		t.Fatalf("read %s: %v", got.CellsPath, err)
	}
	if len(compacted) != len(cells) {
		t.Fatalf("%s holds %d cells, want %d", got.CellsPath, len(compacted), len(cells))
	}
	// The cell's counting columns are the results table, so they are the ones
	// that must survive the round trip intact.
	byID := map[string]bench.Cell{}
	for _, c := range compacted {
		byID[c.ID] = c
	}
	for _, want := range cells {
		got := byID[want.ID]
		if got.Requests != want.Requests || got.Successes != want.Successes ||
			got.Dropped != want.Dropped || got.Failed != want.Failed ||
			got.SLOViolations != want.SLOViolations || got.Clean != want.Clean {
			t.Errorf("cell %s changed in compaction: %+v then %+v", want.ID, want.Summary, got.Summary)
		}
	}
}

// The per-card throttle evidence is a repeated group rather than a flat column,
// because a fleet is named card by card and not counted. It is worth one test of
// its own that the columnar half of the record can hold it: the analysis reads
// Parquet, and evidence that only survives in the JSONL is evidence no figure
// will ever be drawn from.
func TestTheThrottleEvidenceSurvivesCompaction(t *testing.T) {
	dir := t.TempDir()
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies:        []int{1},
		WarmupDriftThreshold: -1,
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Throttled(),
			Interval: time.Millisecond,
			GPUs:     []int{0, 1, 2, 3, 4, 5},
			OwnPIDs:  gputest.OwnFleetPIDs,
		},
	})

	got, err := bench.Compact(dir)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	compacted, err := parquet.ReadFile[bench.Cell](got.CellsPath)
	if err != nil {
		t.Fatalf("read %s: %v", got.CellsPath, err)
	}
	if len(compacted) != len(cells) {
		t.Fatalf("%s holds %d cells, want %d", got.CellsPath, len(compacted), len(cells))
	}

	cell := compacted[0]
	if !cell.Throttled || cell.ClockSamples == 0 {
		t.Errorf("the verdict did not survive compaction: %+v", cell.Throttle)
	}
	over := cell.Throttle.Thermal()
	if len(over) != 1 || over[0].GPU != 3 || over[0].MinSMClockMHz != 960 {
		t.Errorf("the per-card evidence did not survive compaction: %+v", cell.Throttle.GPUs)
	}
	if len(over[0].Reasons) == 0 {
		t.Error("the driver's own words for why the card was held back did not survive compaction")
	}
}

func TestCompactionLeavesTheJSONLInPlace(t *testing.T) {
	dir := t.TempDir()
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	if _, err := bench.Compact(dir); err != nil {
		t.Fatalf("compact: %v", err)
	}

	// Parquet is a derived artifact. The rows are the system of record and
	// every published figure has to stay recomputable from them.
	path := filepath.Join(dir, "cells", cells[0].ID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("compaction removed the rows it was derived from: %v", err)
	}
}

func TestTheRoutersOwnRowsAreCompactedAndJoinToTheHarnessRows(t *testing.T) {
	dir := t.TempDir()
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	// The router appends its rows at the root of the sweep directory. Stand in
	// for that here with rows carrying the request ids the harness recorded.
	rows, err := record.Open[record.Request](filepath.Join(dir, bench.RouterRows))
	if err != nil {
		t.Fatalf("open router rows: %v", err)
	}
	harness, err := parquetRows[bench.Result](t, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range harness {
		if err := rows.Write(record.Request{
			RequestID:        r.RequestID,
			StartedAt:        time.Unix(0, r.StartedAtNs),
			Replica:          r.Replica,
			RouterOverheadNs: 12345,
			Outcome:          r.Outcome,
		}); err != nil {
			t.Fatalf("write router row: %v", err)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close router rows: %v", err)
	}

	got, err := bench.Compact(dir)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if got.RouterRows != len(harness) {
		t.Fatalf("compacted %d router rows for %d harness rows", got.RouterRows, len(harness))
	}

	routerRows, err := parquet.ReadFile[record.Request](got.RouterPath)
	if err != nil {
		t.Fatalf("read %s: %v", got.RouterPath, err)
	}
	// The join is the point: router overhead lives only on the router's side,
	// client-observed TTFT only on the harness's, and belief divergence will
	// need both for the same request.
	byID := map[string]record.Request{}
	for _, r := range routerRows {
		byID[r.RequestID] = r
	}
	for _, h := range harness {
		joined, ok := byID[h.RequestID]
		if !ok {
			t.Fatalf("harness row %s has no router row to join to", h.RequestID)
		}
		if joined.RouterOverheadNs != 12345 {
			t.Errorf("router overhead did not survive compaction: %d", joined.RouterOverheadNs)
		}
	}

	_ = cells
}

// parquetRows reads back the compacted harness rows, compacting first.
func parquetRows[T any](t *testing.T, dir string) ([]bench.Result, error) {
	t.Helper()
	c, err := bench.Compact(dir)
	if err != nil {
		return nil, err
	}
	return parquet.ReadFile[bench.Result](c.RequestsPath)
}
