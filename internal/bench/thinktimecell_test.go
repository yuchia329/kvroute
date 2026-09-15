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
)

// A cell has to say what think time it ran at, and a sweep has to refuse to
// resume cells that ran at another one.
//
// This is the quietest version of the trap that has already cost this project
// two runs. A cell is cached on an id naming the policy, the load point and the
// repetition, and the workload's name catches everything that changes the bytes.
// Think time changes no bytes — the same trace is offered either way — so it is
// in neither. Two think times at one arrival rate therefore agree on every field
// anything compares, and the second sweep resumes the first one's cells, reports
// them cached, sends no requests, and writes a recency curve that is one think
// time plotted twice. Nothing in the logs looks wrong.

// writeCachedCell puts one finished cell record in a sweep directory, as an
// earlier run would have left it.
func writeCachedCell(t *testing.T, dir string, cell bench.Cell) {
	t.Helper()
	cells := filepath.Join(dir, "cells")
	if err := os.MkdirAll(cells, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents, err := json.Marshal(cell)
	if err != nil {
		t.Fatalf("marshal cell: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cells, cell.ID+".json"), contents, 0o644); err != nil {
		t.Fatalf("write cell: %v", err)
	}
}

// sweepAtThinkTime asks a sweep to resume into a directory, and returns what it
// refused to do. It never reaches a router: the cached-cell checks run first, so
// a refusal here is the one under test and a nil means the sweep got past them.
func sweepAtThinkTime(t *testing.T, dir string, think time.Duration) error {
	t.Helper()
	workload := bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1})
	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:          dir,
		Target:       "http://127.0.0.1:1", // never reached
		Policy:       "round_robin",
		ArrivalRates: []float64{8},
		ThinkTime:    think,
		Repetitions:  1,
		CellDuration: 20 * time.Millisecond,
		Workload:     workload,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return err
}

// cachedOpenLoopCell is a finished open-loop cell at one think time.
func cachedOpenLoopCell(think time.Duration) bench.Cell {
	return bench.Cell{
		ID:          "round_robin-a8-r1",
		Policy:      "round_robin",
		Driver:      bench.OpenLoopDriver,
		ArrivalRate: 8,
		Repetition:  1,
		Workload:    bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}).Name(),
		ThinkTimeNs: think.Nanoseconds(),
	}
}

func TestASweepRefusesCellsRunAtAnotherThinkTime(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedOpenLoopCell(5*time.Second))

	err := sweepAtThinkTime(t, dir, 30*time.Second)
	if err == nil {
		t.Fatal("a sweep resumed cells recorded at a different think time, so the second think time would have sent nothing")
	}
	// The refusal has to name both, or the reader cannot tell which directory
	// holds which and the repair is guesswork.
	if !strings.Contains(err.Error(), "5s") || !strings.Contains(err.Error(), "30s") {
		t.Errorf("the refusal does not name both think times: %v", err)
	}
}

func TestASweepAtTheCellsOwnThinkTimeResumes(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedOpenLoopCell(30*time.Second))

	// Past the cached-cell checks, so whatever it fails on next is the router it
	// was never given — not the think time.
	err := sweepAtThinkTime(t, dir, 30*time.Second)
	if err != nil && strings.Contains(err.Error(), "think time") {
		t.Fatalf("a matching think time was refused: %v", err)
	}
}

// Zero is unstated, not instant. Every open-loop cell recorded before this
// column existed carries no think time, and refusing them would invalidate the
// whole open-loop half of the definitive run over a field nobody wrote.
func TestCellsRecordedBeforeThinkTimeWasWrittenStillResume(t *testing.T) {
	dir := t.TempDir()
	cell := cachedOpenLoopCell(0)
	writeCachedCell(t, dir, cell)

	err := sweepAtThinkTime(t, dir, 30*time.Second)
	if err != nil && strings.Contains(err.Error(), "think time") {
		t.Fatalf("a cell recorded before the think-time column existed was refused: %v", err)
	}
}

// A configured zero means the driver's default, so a sweep left at the default
// and one that spelled the default out are the same sweep and must resume each
// other. Recording the configured value raw would make them differ.
func TestTheDefaultThinkTimeIsRecordedResolvedRatherThanAsZero(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedOpenLoopCell(bench.DefaultThinkTime))

	if err := sweepAtThinkTime(t, dir, 0); err != nil && strings.Contains(err.Error(), "think time") {
		t.Fatalf("an unset think time was refused against cells run at the default: %v", err)
	}
	if err := sweepAtThinkTime(t, dir, bench.DefaultThinkTime); err != nil && strings.Contains(err.Error(), "think time") {
		t.Fatalf("the default spelled out was refused against cells run at the default: %v", err)
	}
}

// A closed-loop cell has no think time to disagree about: its next turn goes out
// when the last response lands. Recording one would be a column claiming the
// driver honoured a gap it ignores.
func TestAClosedLoopCellRecordsNoThinkTime(t *testing.T) {
	dir := t.TempDir()
	cell := cachedOpenLoopCell(0)
	cell.ID = "round_robin-c32-r1"
	cell.Driver = bench.ClosedLoopDriver
	cell.ArrivalRate = 0
	cell.Concurrency = 32
	writeCachedCell(t, dir, cell)

	if err := sweepAtThinkTime(t, dir, 30*time.Second); err != nil && strings.Contains(err.Error(), "think time") {
		t.Fatalf("a closed-loop cell was refused over a think time it never used: %v", err)
	}
}
