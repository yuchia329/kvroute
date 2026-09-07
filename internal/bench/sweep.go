package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
)

// ConcurrencySweep is the scaling axis: how each policy's latency degrades as
// offered load rises.
var ConcurrencySweep = []int{1, 4, 8, 16, 32, 64, 128, 256}

// ClosedLoopDriver names the driver in a cell record, so no table can be read
// without knowing which one produced it. A closed-loop driver throttles itself
// when the fleet slows, so its tail is systematically optimistic; the headline
// goodput number comes from the open-loop driver instead.
const ClosedLoopDriver = "closed_loop"

// Cell is one benchmark data point: a fixed policy, concurrency and repetition,
// with its own summary and its own contamination evidence.
type Cell struct {
	ID          string `json:"id" parquet:"id"`
	Policy      string `json:"policy" parquet:"policy"`
	Concurrency int    `json:"concurrency" parquet:"concurrency"`
	Repetition  int    `json:"repetition" parquet:"repetition"`
	Driver      string `json:"driver" parquet:"driver"`
	Workload    string `json:"workload" parquet:"workload"`

	StartedAtNs int64 `json:"started_at_ns" parquet:"started_at_ns"`
	EndedAtNs   int64 `json:"ended_at_ns" parquet:"ended_at_ns"`

	Summary       `json:"summary"`
	Contamination `json:"contamination"`
}

// CellID is the cell's identity and its cache key. It is derived from the axes
// alone, so re-running a sweep with the same axes finds the same cells.
func CellID(policy string, concurrency, repetition int) string {
	return fmt.Sprintf("%s-c%d-r%d", policy, concurrency, repetition)
}

// SweepConfig configures a concurrency sweep.
type SweepConfig struct {
	// Dir holds the sweep's cells. An interrupted sweep resumes from what is
	// already in it.
	Dir string
	// Target is the router's base URL.
	Target string
	// Policy names the policy the router is running. The sweep does not set it
	// — the router is started with it — so this is a label that has to be told
	// the truth.
	Policy string

	Concurrencies []int
	Repetitions   int
	CellDuration  time.Duration
	Warmup        int
	// Settle is how long to wait between cells, so one cell's tail does not
	// land inside the next one's window.
	Settle time.Duration

	SLO              SLO
	FailureThreshold float64
	Workload         Workload
	Contamination    ContaminationConfig

	Log *slog.Logger
}

// RunSweep runs every cell of the concurrency sweep and returns them in order.
//
// Cells already on disk are loaded rather than re-run. The sweep is hours long
// on a shared box, so resumability is not a convenience: without it an
// interruption in the eleventh hour costs the ten before it.
func RunSweep(ctx context.Context, cfg SweepConfig) ([]Cell, error) {
	if cfg.Dir == "" {
		return nil, errors.New("bench: a sweep directory is required")
	}
	if cfg.Policy == "" {
		return nil, errors.New("bench: the policy the router is running must be named")
	}
	if len(cfg.Concurrencies) == 0 {
		cfg.Concurrencies = ConcurrencySweep
	}
	if cfg.Repetitions <= 0 {
		cfg.Repetitions = 1
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.Workload == nil {
		cfg.Workload = NewFixedWorkload(FixedWorkload{})
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	cfg.Contamination.Log = cfg.Log

	cellDir := filepath.Join(cfg.Dir, "cells")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		return nil, fmt.Errorf("bench: create %s: %w", cellDir, err)
	}

	var cells []Cell
	for _, concurrency := range cfg.Concurrencies {
		for repetition := 1; repetition <= cfg.Repetitions; repetition++ {
			id := CellID(cfg.Policy, concurrency, repetition)

			if cached, ok := loadCell(cellDir, id); ok {
				cfg.Log.Info("cell is cached, not re-running", "cell", id,
					"goodput_rps", cached.GoodputRPS, "flagged", cached.Flagged)
				cells = append(cells, cached)
				continue
			}
			if err := ctx.Err(); err != nil {
				return cells, err
			}
			if len(cells) > 0 && cfg.Settle > 0 {
				time.Sleep(cfg.Settle)
			}

			cell, err := runCell(ctx, cfg, cellDir, id, concurrency, repetition)
			if err != nil {
				return cells, err
			}
			cells = append(cells, cell)
		}
	}
	return cells, nil
}

// runCell runs one cell and writes it to disk.
func runCell(ctx context.Context, cfg SweepConfig, cellDir, id string, concurrency, repetition int) (Cell, error) {
	// Rows stream to a partial file and are renamed into place only once the
	// cell has finished. A crash therefore leaves readable partial data under a
	// name that is visibly incomplete, and never leaves a half-run cell looking
	// like a cached one.
	rowPath := filepath.Join(cellDir, id+".jsonl")
	partialPath := rowPath + ".partial"
	rows, err := record.Open[Result](partialPath)
	if err != nil {
		return Cell{}, err
	}

	cfg.Log.Info("running cell", "cell", id, "concurrency", concurrency, "duration", cfg.CellDuration)
	started := time.Now()
	watcher := Watch(ctx, cfg.Contamination)

	results, runErr := RunClosedLoop(ctx, DriverConfig{
		Target:      cfg.Target,
		Concurrency: concurrency,
		Duration:    cfg.CellDuration,
		Warmup:      cfg.Warmup,
		Workload:    cfg.Workload,
		Rows:        rows,
		Labels: Labels{
			CellID:      id,
			Policy:      cfg.Policy,
			Concurrency: concurrency,
			Repetition:  repetition,
		},
		Log: cfg.Log,
	})
	contamination := watcher.Stop()
	ended := time.Now()

	if closeErr := rows.Close(); closeErr != nil && runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return Cell{}, fmt.Errorf("bench: cell %s: %w", id, runErr)
	}
	if err := os.Rename(partialPath, rowPath); err != nil {
		return Cell{}, fmt.Errorf("bench: cell %s: %w", id, err)
	}

	cell := Cell{
		ID:            id,
		Policy:        cfg.Policy,
		Concurrency:   concurrency,
		Repetition:    repetition,
		Driver:        ClosedLoopDriver,
		Workload:      cfg.Workload.Name(),
		StartedAtNs:   started.UnixNano(),
		EndedAtNs:     ended.UnixNano(),
		Summary:       Summarize(results, cfg.SLO, cfg.FailureThreshold),
		Contamination: contamination,
	}
	// Contamination flags the cell just as a failure rate does: an unclean cell
	// is discarded and re-run, never averaged in.
	if !cell.Clean {
		switch {
		case cell.GPUSamples == 0:
			cell.add("the GPUs were never sampled, so this cell carries no cleanliness evidence")
		case cell.ProbeErrors > 0:
			cell.add(fmt.Sprintf("%d GPU probes failed, so cleanliness is unproven", cell.ProbeErrors))
		default:
			cell.add(fmt.Sprintf("%d foreign processes on the fleet's GPUs, peak %d MiB: %v",
				len(cell.ForeignProcs), cell.MaxForeignGPUMemMiB, cell.ForeignProcs))
		}
	}

	if err := writeCell(cellDir, cell); err != nil {
		return Cell{}, err
	}
	cfg.Log.Info("cell complete", "cell", id,
		"requests", cell.Requests, "successes", cell.Successes,
		"dropped", cell.Dropped, "failed", cell.Failed, "slo_violations", cell.SLOViolations,
		"goodput_rps", cell.GoodputRPS, "clean", cell.Clean, "flagged", cell.Flagged)
	return cell, nil
}

// loadCell reads a completed cell, if there is one.
func loadCell(cellDir, id string) (Cell, bool) {
	contents, err := os.ReadFile(filepath.Join(cellDir, id+".json"))
	if err != nil {
		return Cell{}, false
	}
	var cell Cell
	if err := json.Unmarshal(contents, &cell); err != nil {
		// A truncated record is not a cached cell. Re-running costs one cell;
		// trusting a half-written one costs the result.
		return Cell{}, false
	}
	return cell, true
}

// writeCell writes the record that marks a cell complete. It is written last
// and renamed into place, so its presence means the cell finished.
func writeCell(cellDir string, cell Cell) error {
	contents, err := json.MarshalIndent(cell, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: marshal cell %s: %w", cell.ID, err)
	}
	path := filepath.Join(cellDir, cell.ID+".json")
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("bench: write cell %s: %w", cell.ID, err)
	}
	return os.Rename(tmp, path)
}
