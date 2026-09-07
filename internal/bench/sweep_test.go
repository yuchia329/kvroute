package bench_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

// sweepUnderTest runs a sweep against a fake fleet in a temporary directory,
// returning the cells, the directory and the replica's request counter.
func sweepUnderTest(t *testing.T, dir string, cfg bench.SweepConfig) ([]bench.Cell, *inflight) {
	t.Helper()
	target, _, counted := fleetUnderTest(t, fakereplica.Config{})

	cfg.Dir = dir
	cfg.Target = target
	if cfg.Policy == "" {
		cfg.Policy = "round_robin"
	}
	if cfg.Concurrencies == nil {
		cfg.Concurrencies = []int{1, 2}
	}
	if cfg.Repetitions == 0 {
		cfg.Repetitions = 1
	}
	if cfg.CellDuration == 0 {
		cfg.CellDuration = 20 * time.Millisecond
	}
	if cfg.Workload == nil {
		cfg.Workload = bench.NewFixedWorkload(bench.FixedWorkload{OutputTokens: 2})
	}
	cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	cells, err := bench.RunSweep(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run sweep: %v", err)
	}
	return cells, counted
}

func TestASweepResumesFromCachedCellsWithoutRecomputingThem(t *testing.T) {
	dir := t.TempDir()

	first, firstReplica := sweepUnderTest(t, dir, bench.SweepConfig{})
	if len(first) != 2 {
		t.Fatalf("first pass produced %d cells, want 2", len(first))
	}
	if firstReplica.total.Load() == 0 {
		t.Fatal("the first pass sent no requests")
	}

	// A second pass over the same directory: a sweep that was interrupted after
	// eleven hours must not start the twelfth from the beginning.
	second, secondReplica := sweepUnderTest(t, dir, bench.SweepConfig{})

	if got := secondReplica.total.Load(); got != 0 {
		t.Errorf("the second pass sent %d requests, want none: cached cells were recomputed", got)
	}
	if len(second) != len(first) {
		t.Fatalf("second pass produced %d cells, want %d", len(second), len(first))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Requests != second[i].Requests {
			t.Errorf("cell %s came back changed: %+v then %+v", first[i].ID, first[i].Summary, second[i].Summary)
		}
	}
}

func TestASweepRecomputesACellThatNeverFinished(t *testing.T) {
	dir := t.TempDir()

	first, _ := sweepUnderTest(t, dir, bench.SweepConfig{})

	// Simulate a crash partway through one cell: its rows are on disk but the
	// cell record that marks it complete was never written.
	victim := first[1].ID
	if err := os.Remove(filepath.Join(dir, "cells", victim+".json")); err != nil {
		t.Fatalf("remove cell record: %v", err)
	}

	_, replica := sweepUnderTest(t, dir, bench.SweepConfig{})

	if replica.total.Load() == 0 {
		t.Error("the interrupted cell was treated as cached and never re-run")
	}
}

func TestTheRowsOfEveryCellAreOnDiskAsJSONL(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	path := filepath.Join(dir, "cells", cells[0].ID+".jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cell rows: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != cells[0].Requests+cells[0].Warmup {
		t.Errorf("%s holds %d rows, want %d", path, len(lines), cells[0].Requests+cells[0].Warmup)
	}
	var row bench.Result
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("row is not valid JSON: %v", err)
	}
	if row.CellID != cells[0].ID {
		t.Errorf("row is stamped with cell %q, want %q", row.CellID, cells[0].ID)
	}
}

func TestACellRecordsTheForeignProcessesItSawAndIsNotClean(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{1},
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Contaminated(),
			Interval: time.Millisecond,
			GPUs:     []int{0},
		},
	})

	got := cells[0]
	if got.Clean {
		t.Error("a cell that ran beside another user's process reported itself clean")
	}
	if len(got.ForeignProcs) == 0 {
		t.Error("the cell records no foreign process")
	}
	if got.MaxForeignGPUMemMiB != 8999 {
		t.Errorf("peak foreign memory is %d MiB, want 8999", got.MaxForeignGPUMemMiB)
	}
	if !got.Flagged {
		t.Error("an unclean cell was not flagged")
	}
}

func TestACellIsNotClaimedCleanWhenTheGPUsWereNeverSampled(t *testing.T) {
	dir := t.TempDir()

	// No prober: running against fakes on a machine with no GPU. Reporting
	// clean here would be the same word for "nothing was found" and "nothing
	// was looked for".
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	got := cells[0]
	if got.Clean {
		t.Error("a cell whose GPUs were never sampled reported itself clean")
	}
	if got.GPUSamples != 0 {
		t.Errorf("recorded %d GPU samples with no prober configured", got.GPUSamples)
	}
	if !got.Flagged {
		t.Error("a cell with no contamination evidence was not flagged")
	}
}

func TestACleanCellSaysSo(t *testing.T) {
	dir := t.TempDir()

	// Our own fleet, holding both cards, and nothing else.
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{1},
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Fleet(),
			Interval: time.Millisecond,
			GPUs:     []int{0, 1},
			OwnPIDs:  gputest.OwnFleetPIDs,
		},
	})

	got := cells[0]
	if !got.Clean {
		t.Errorf("our own fleet was reported as contamination: %v", got.ForeignProcs)
	}
	if got.GPUSamples == 0 {
		t.Error("the cell recorded no GPU samples")
	}
	if got.Flagged {
		t.Errorf("a clean cell was flagged: %v", got.FlagReasons)
	}
}
