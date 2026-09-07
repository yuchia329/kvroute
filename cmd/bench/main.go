// Command bench sweeps concurrency against a running fleet.
//
// It holds a fixed number of virtual users at each of eight concurrency levels,
// records one row per request, samples the GPUs throughout so every cell
// carries its own cleanliness evidence, and compacts the run to Parquet at the
// end.
//
//	bench -router http://127.0.0.1:8080 \
//	      -dir runs/concurrency \
//	      -policy round_robin \
//	      -cell-duration 60s -repetitions 3
//
// It resumes: cells already complete in -dir are loaded rather than re-run, so
// an interruption in the eleventh hour does not cost the ten before it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/gpu"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		target      = flag.String("router", "http://127.0.0.1:8080", "the router to drive")
		dir         = flag.String("dir", "runs/concurrency", "where cells are written and resumed from")
		policyName  = flag.String("policy", "round_robin", "the policy the router is running; recorded as the cell's label")
		levels      = flag.String("concurrency", "1,4,8,16,32,64,128,256", "concurrency levels to sweep")
		repetitions = flag.Int("repetitions", 3, "repetitions per level; p99 is noisy at low sample counts")
		duration    = flag.Duration("cell-duration", 60*time.Second, "how long each cell keeps starting new turns")
		warmup      = flag.Int("warmup", 5, "requests per virtual user marked as warm-up and excluded from the summary")
		settle      = flag.Duration("settle", 10*time.Second, "pause between cells, so one cell's tail stays out of the next one's window")

		sloTTFT   = flag.Duration("slo-ttft", 0, "TTFT threshold; unset means no SLO was applied and goodput is not reported")
		sloITL    = flag.Duration("slo-itl", 0, "inter-token latency threshold, evaluated against each request's median gap")
		failureAt = flag.Float64("failure-threshold", bench.DefaultFailureThreshold, "failure rate above which a cell is flagged")

		promptBytes  = flag.Int("prompt-bytes", 2048, "approximate prompt size per request")
		outputTokens = flag.Int("output-tokens", 64, "max_tokens per request")
		seed         = flag.Uint64("seed", 1, "workload seed; the same seed sends the same bytes")

		sampleGPUs = flag.Bool("sample-gpus", true, "sample nvidia-smi during each cell for contamination evidence")
		gpus       = flag.Int("gpus", 6, "how many GPUs the fleet uses")
		interval   = flag.Duration("sample-interval", bench.DefaultSampleInterval, "how often to sample the GPUs")
		pidGlob    = flag.String("replica-pids", "run/replica-*.pid", "glob of the fleet's pid files, used to tell our own processes from foreign ones")

		logLevel = flag.String("log-level", "info", "log level: debug, info, warn or error")
	)
	flag.Parse()

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("unknown log level %q", *logLevel)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	concurrencies, err := parseLevels(*levels)
	if err != nil {
		return err
	}

	contamination := bench.ContaminationConfig{Interval: *interval}
	if *sampleGPUs {
		contamination.Prober = gpu.New()
		contamination.GPUs = indexes(*gpus)
		if contamination.OwnPIDs, err = replicaPIDs(*pidGlob); err != nil {
			return err
		}
		if len(contamination.OwnPIDs) == 0 {
			// Without the fleet's own pids every replica would be classified as
			// a foreign process and every cell would be unclean, which is worse
			// than not sampling: it looks like evidence.
			return fmt.Errorf("no replica pid files matched %q. Start the fleet with ops/fleet.sh up, or pass -sample-gpus=false to run without contamination evidence", *pidGlob)
		}
		log.Info("sampling GPUs for contamination", "gpus", *gpus, "replica_pids", contamination.OwnPIDs, "interval", *interval)
	} else {
		log.Warn("GPU sampling is off: every cell will record that its cleanliness is unproven")
	}

	// SIGINT stops the sweep between cells rather than mid-cell, so an
	// interrupted run leaves whole cells behind and resumes cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cells, sweepErr := bench.RunSweep(ctx, bench.SweepConfig{
		Dir:              *dir,
		Target:           *target,
		Policy:           *policyName,
		Concurrencies:    concurrencies,
		Repetitions:      *repetitions,
		CellDuration:     *duration,
		Warmup:           *warmup,
		Settle:           *settle,
		SLO:              bench.SLO{TTFT: *sloTTFT, ITL: *sloITL},
		FailureThreshold: *failureAt,
		Workload: bench.NewFixedWorkload(bench.FixedWorkload{
			PromptBytes:  *promptBytes,
			OutputTokens: *outputTokens,
			Seed:         *seed,
		}),
		Contamination: contamination,
		Log:           log,
	})

	// Report and compact whatever finished, even if the sweep stopped early:
	// the cells that ran are still cells, and they are what the next pass
	// resumes from.
	if len(cells) > 0 {
		table := bench.Table(cells)
		fmt.Print("\n" + table)
		path := filepath.Join(*dir, "results.md")
		if err := os.WriteFile(path, []byte(header(*policyName, *sloTTFT, *sloITL)+table), 0o644); err != nil {
			return err
		}
		compaction, err := bench.Compact(*dir)
		if err != nil {
			return err
		}
		log.Info("compacted the run",
			"requests", compaction.Requests, "cells", compaction.Cells,
			"requests_parquet", compaction.RequestsPath, "cells_parquet", compaction.CellsPath,
			"results", path)
	}
	return sweepErr
}

// header states what produced the table, because a latency table without its
// driver and its SLO is not interpretable.
func header(policy string, ttft, itl time.Duration) string {
	slo := "none applied — goodput is not reported and SLO violations were not counted"
	if ttft > 0 || itl > 0 {
		slo = fmt.Sprintf("TTFT < %v, inter-token p50 < %v", ttft, itl)
	}
	return fmt.Sprintf("# Concurrency sweep — %s\n\nDriver: closed-loop (offered load is an outcome, so the tail here is\noptimistic; the headline goodput number comes from the open-loop driver).\n\nSLO: %s\n\n", policy, slo)
}

func parseLevels(spec string) ([]int, error) {
	var levels []int
	for field := range strings.SplitSeq(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("concurrency level %q is not a positive integer", field)
		}
		levels = append(levels, n)
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("no concurrency levels given")
	}
	return levels, nil
}

func indexes(n int) []int {
	out := make([]int, 0, n)
	for i := range n {
		out = append(out, i)
	}
	return out
}

// replicaPIDs reads the fleet's supervisor pids off its pid files. The process
// holding a card is the engine child of one of these, so ownership is resolved
// by ancestry rather than by matching these directly.
func replicaPIDs(glob string) ([]int, error) {
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("bad pid file glob %q: %w", glob, err)
	}
	var pids []int
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err != nil {
			return nil, fmt.Errorf("%s does not hold a pid: %w", path, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
