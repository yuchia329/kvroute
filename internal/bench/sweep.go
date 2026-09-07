package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// ConcurrencySweep is the scaling axis: how each policy's latency degrades as
// offered load rises.
var ConcurrencySweep = []int{1, 4, 8, 16, 32, 64, 128, 256}

// FormatLevels renders load levels into the comma-separated spec the commands
// take, so a flag's default can be the package's own value rather than a second
// copy of it that drifts.
func FormatLevels(levels []int) string {
	fields := make([]string, 0, len(levels))
	for _, n := range levels {
		fields = append(fields, strconv.Itoa(n))
	}
	return strings.Join(fields, ",")
}

// ParseLevels reads a comma-separated spec of load levels.
//
// Here rather than in each command because both take the same spec, and two
// parsers for one format is two places for "0" or "-4" to be accepted by one
// and rejected by the other.
func ParseLevels(spec string) ([]int, error) {
	var levels []int
	for field := range strings.SplitSeq(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bench: load level %q is not a positive integer", field)
		}
		levels = append(levels, n)
	}
	if len(levels) == 0 {
		return nil, errors.New("bench: no load levels given")
	}
	return levels, nil
}

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

// CellWorkloadOffset is the slice of the workload's user space a cell sends
// from. It is derived from the axes alone — deliberately not from the policy —
// so a re-run of a cell sends its own bytes again and no cell ever re-sends
// another's.
func CellWorkloadOffset(concurrency, repetition int) int {
	return (repetition*1024 + concurrency) * WorkloadStride
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
	// Warmup is the slice at the start of each cell whose rows are recorded but
	// excluded from the summary.
	Warmup time.Duration
	// FleetWarmup is how many requests to send to each replica directly,
	// before the first cell, to force its first real forward pass. Zero skips
	// it.
	FleetWarmup int
	// Settle is how long to wait between cells, so one cell's tail does not
	// land inside the next one's window.
	Settle time.Duration

	// Replicas are the base URLs of every replica the router fronts. Each is
	// asked for /health before the first cell runs. Empty skips the check.
	Replicas []string

	SLO              SLO
	FailureThreshold float64
	// WarmupDriftThreshold flags a cell whose measured window was still
	// speeding up. Zero uses DefaultWarmupDriftThreshold.
	WarmupDriftThreshold float64
	Workload             Workload
	Contamination        ContaminationConfig

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

	if err := checkFleet(ctx, cfg); err != nil {
		return nil, err
	}
	if err := WarmReplicas(ctx, WarmConfig{
		Replicas: cfg.Replicas,
		Requests: cfg.FleetWarmup,
		Workload: cfg.Workload,
		Log:      cfg.Log,
	}); err != nil {
		return nil, err
	}

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

func (cfg SweepConfig) summaryOptions() SummaryOptions {
	return SummaryOptions{
		SLO:                  cfg.SLO,
		FailureThreshold:     cfg.FailureThreshold,
		WarmupDriftThreshold: cfg.WarmupDriftThreshold,
	}
}

// WarmConfig configures the direct warm-up.
type WarmConfig struct {
	// Replicas are the base URLs to warm. Empty skips the warm-up.
	Replicas []string
	// Requests is how many to send to each. Zero skips the warm-up.
	Requests int
	Workload Workload
	Log      *slog.Logger
}

// WarmReplicas sends a few requests to each replica directly, before the first
// measurement, so that every replica has done a real forward pass before any
// measured request reaches it.
//
// Directly, not through the router, because the router decides where a request
// goes and the harness does not get a say. Warming through it at concurrency 1
// under round-robin sends the first five requests to replicas 0..4 and leaves
// replica 5 to serve the first *measured* request cold — the warm-up would
// manufacture the very cold start it exists to prevent, in the cell that
// defines the latency floor.
//
// This is a handful of requests once per run, not per cell: what it covers —
// lazy allocation and first-execution kernel paths that survive /health —
// happens once per process. Per-cell transients are what SweepConfig.Warmup is
// for.
//
// The characterization pass needs it for the same reason and more sharply: the
// concurrency-1 measurement it takes is the latency floor every SLO is derived
// from, so a cold first forward pass landing inside it would put a one-off
// compile into the threshold every later cell is judged against.
func WarmReplicas(ctx context.Context, cfg WarmConfig) error {
	if cfg.Requests <= 0 || len(cfg.Replicas) == 0 {
		return nil
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	errs := make([]error, len(cfg.Replicas))

	var wg sync.WaitGroup
	for i, base := range cfg.Replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range cfg.Requests {
				// Turn index n keeps the bodies distinct, so the replica is not
				// answering the same request from its own prefix cache every
				// time and skipping the work being warmed.
				if err := warmOnce(ctx, client, base, cfg.Workload.Next(i, n)); err != nil {
					errs[i] = fmt.Errorf("%s: %w", base, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("bench: warming the fleet failed, so a replica cannot serve: %w", err)
	}
	cfg.Log.Info("fleet warmed", "replicas", len(cfg.Replicas), "requests_each", cfg.Requests)
	return nil
}

func warmOnce(ctx context.Context, client *http.Client, base string, turn Turn) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(base, "/")+router.ChatCompletionsPath, bytes.NewReader(turn.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drained rather than discarded unread, so the warm-up pays the whole cost
	// of a response the way a measured request will.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// checkFleet refuses to start a sweep unless every replica answers /health.
//
// Nothing below this ejects a dead replica — health checking and ejection are
// their own ticket — so round-robin would keep dispatching to it, the router
// would fail to place one request in six, and the cell would fill with drops.
// The sweep is hours long, so the difference between catching that here and
// catching it in the results is a wasted night on a box that is only idle until
// the 20th.
func checkFleet(ctx context.Context, cfg SweepConfig) error {
	if len(cfg.Replicas) == 0 {
		cfg.Log.Warn("no replica URLs given, so the fleet was not checked before the sweep")
		return nil
	}
	if err := CheckReplicas(ctx, cfg.Replicas); err != nil {
		return err
	}
	cfg.Log.Info("fleet is up", "replicas", len(cfg.Replicas))
	return nil
}

// CheckReplicas refuses to proceed unless every replica answers /health.
//
// Exported for the same reason WarmReplicas is: the characterization pass has
// to establish that it is measuring the whole fleet before it claims anything
// about whether the fleet is interchangeable.
func CheckReplicas(ctx context.Context, replicas []string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	var down []string
	for _, base := range replicas {
		if err := ping(ctx, client, base); err != nil {
			down = append(down, fmt.Sprintf("%s (%v)", base, err))
		}
	}
	if len(down) > 0 {
		return fmt.Errorf("bench: %d of %d replicas did not answer /health, so this would measure an incomplete fleet: %s",
			len(down), len(replicas), strings.Join(down, "; "))
	}
	return nil
}

func ping(ctx context.Context, client *http.Client, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
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
		// Each cell gets its own slice of the workload's user space, keyed on
		// the axes it varies and not on the policy. Two cells that differ only
		// in repetition or concurrency therefore send different bytes — without
		// it the second repetition re-sends the first one's prompts and reads
		// them back out of the replica's prefix cache — while the same cell
		// under two policies sends identical bytes, which is what makes the
		// policies comparable at all.
		Workload: Shifted(cfg.Workload, CellWorkloadOffset(concurrency, repetition)),
		Rows:     rows,
		Labels:   Labels{CellID: id, Policy: cfg.Policy, Repetition: repetition},
		Log:      cfg.Log,
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
		Summary:       Summarize(results, cfg.summaryOptions()),
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
	for _, reason := range cell.Contamination.Reasons() {
		cell.Flag(reason)
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
	cached.Summary = Summarize(rows, cfg.summaryOptions())
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
