package bench_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/yuchia329/kvroute/internal/bench"
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
