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

			cached, ok := loadCell(cellDir, id)
			switch {
			case ok && cached.Contaminated():
				// §6: any cell with a foreign process on any of the six cards is
				// discarded and re-run, never averaged in. Leaving it cached
				// would make "re-run" mean "delete the file by hand first", so
				// the evidence is moved out of the way and the cell recomputed.
				if err := discard(cfg.Dir, cellDir, id); err != nil {
					return cells, err
				}
				cfg.Log.Warn("cached cell was contaminated, discarding and re-running", "cell", id,
					"foreign_procs", cached.ForeignProcs, "probe_errors", cached.ProbeErrors)
			case ok && !cached.matchesSLO(cfg.SLO):
				// The SLO is derived from the measured concurrency-1 floor, so
				// it arrives after the cells it is applied to. SLO violation is
				// a property of the rows, not of the run, so it is recomputed
				// from them rather than costing another hour of GPU time.
				recomputed, err := resummarize(cellDir, cached, cfg)
				if err != nil {
					return cells, err
				}
				cfg.Log.Info("cell is cached, resummarised against the current SLO", "cell", id,
					"goodput_rps", recomputed.GoodputRPS, "slo_violations", recomputed.SLOViolations)
				cells = append(cells, recomputed)
				continue
			case ok:
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
		Labels:      Labels{CellID: id, Policy: cfg.Policy, Repetition: repetition},
		Log:         cfg.Log,
	})
	contamination := watcher.Stop()
	ended := time.Now()

	if closeErr := rows.Close(); closeErr != nil && runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return Cell{}, fmt.Errorf("bench: cell %s: %w", id, runErr)
	}
	// An interrupted cell is not a cell. Its rows stay on disk under the
	// visibly incomplete .partial name, and neither they nor a cell record are
	// renamed into place — otherwise the next pass would find a cache entry for
	// a cell that was cut off partway and never re-run it.
	if err := ctx.Err(); err != nil {
		cfg.Log.Warn("cell was interrupted and will be re-run, not cached", "cell", id, "partial_rows", partialPath)
		return Cell{}, fmt.Errorf("bench: cell %s was interrupted: %w", id, err)
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
	flagContamination(&cell)

	if err := writeCell(cellDir, cell); err != nil {
		return Cell{}, err
	}
	cfg.Log.Info("cell complete", "cell", id,
		"requests", cell.Requests, "successes", cell.Successes,
		"dropped", cell.Dropped, "failed", cell.Failed, "slo_violations", cell.SLOViolations,
		"goodput_rps", cell.GoodputRPS, "clean", cell.Clean, "flagged", cell.Flagged)
	return cell, nil
}

// flagContamination adds the reasons the GPUs give for not averaging this cell
// in with the others. Summarize flags what the rows say; this flags what the
// cards said.
func flagContamination(cell *Cell) {
	for _, reason := range cell.Contamination.reasons() {
		cell.add(reason)
	}
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

// matchesSLO reports whether a cached cell was summarised against the SLO now
// configured.
func (c Cell) matchesSLO(slo SLO) bool {
	return c.SLOTTFTNs == slo.TTFT.Nanoseconds() && c.SLOITLNs == slo.ITL.Nanoseconds()
}

// resummarize recomputes a cached cell's summary from its rows under the
// current SLO, keeping the contamination evidence the original run gathered.
// The rows are the system of record precisely so this is possible.
func resummarize(cellDir string, cached Cell, cfg SweepConfig) (Cell, error) {
	rows, err := decodeFile[Result](filepath.Join(cellDir, cached.ID+".jsonl"))
	if err != nil {
		return Cell{}, err
	}
	cached.Summary = Summarize(rows, cfg.SLO, cfg.FailureThreshold)
	flagContamination(&cached)
	if err := writeCell(cellDir, cached); err != nil {
		return Cell{}, err
	}
	return cached, nil
}

// discard moves a contaminated cell's record and rows out of the cell
// directory, so the cell is recomputed while the evidence for why it was thrown
// away survives. It lands outside cells/ so compaction cannot pick it up as
// though it were a result.
func discard(dir, cellDir, id string) error {
	graveyard := filepath.Join(dir, "discarded")
	if err := os.MkdirAll(graveyard, 0o755); err != nil {
		return fmt.Errorf("bench: create %s: %w", graveyard, err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, ext := range []string{".json", ".jsonl"} {
		from := filepath.Join(cellDir, id+ext)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		to := filepath.Join(graveyard, id+"-"+stamp+ext)
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("bench: discard %s: %w", from, err)
		}
	}
	return nil
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
