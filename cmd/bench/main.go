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
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/gpu"
)

func main() {
	if err := run(); err != nil {
		// Not prefixed here: every error this returns already names itself,
		// and prefixing again produced "bench: bench: ...".
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		target       = flag.String("router", "http://127.0.0.1:8080", "the router to drive")
		replicaSpecs = flag.String("replicas", "", "the same -replicas spec the router was given; each is asked for /health before the sweep starts")
		dir          = flag.String("dir", "runs/concurrency", "where cells are written and resumed from")
		policyName   = flag.String("policy", "round_robin", "the policy the router is running; recorded as the cell's label")
		levels       = flag.String("concurrency", bench.FormatLevels(bench.ConcurrencySweep), "concurrency levels to sweep")
		repetitions  = flag.Int("repetitions", 3, "repetitions per level; p99 is noisy at low sample counts")
		duration     = flag.Duration("cell-duration", 60*time.Second, "how long each cell keeps starting new turns")
		warmup       = flag.Duration("warmup", 10*time.Second, "slice at the start of each cell whose rows are recorded but excluded from the summary; a duration, so every concurrency level forfeits the same share of its window")
		fleetWarmup  = flag.Int("fleet-warmup", 3, "requests sent to each replica directly before the first cell, so every replica has done a real forward pass")
		settle       = flag.Duration("settle", 10*time.Second, "pause between cells, so one cell's tail stays out of the next one's window")

		sloTTFT   = flag.Duration("slo-ttft", 0, "TTFT threshold; unset means no SLO was applied and goodput is not reported")
		sloITL    = flag.Duration("slo-itl", 0, "inter-token latency threshold, evaluated against each request's median gap")
		failureAt = flag.Float64("failure-threshold", bench.DefaultFailureThreshold, "failure rate above which a cell is flagged")
		driftAt   = flag.Float64("warmup-drift-threshold", bench.DefaultWarmupDriftThreshold, "how much slower a cell's first measured half may be than its second before it is flagged as under-warmed; negative disables the check")

		model        = flag.String("model", "", "the model the replicas serve; no default, it is pinned in ops/versions.env")
		promptBytes  = flag.Int("prompt-bytes", 2048, "approximate prompt size per request")
		outputTokens = flag.Int("output-tokens", 64, "max_tokens per request")
		seed         = flag.Uint64("seed", 1, "workload seed; the same seed sends the same bytes")

		sampleGPUs = flag.Bool("sample-gpus", true, "sample nvidia-smi during each cell for contamination evidence")
		gpus       = flag.Int("gpus", 0, "how many GPUs the fleet uses; no default, it is REPLICA_COUNT in ops/versions.env")
		interval   = flag.Duration("sample-interval", bench.DefaultSampleInterval, "how often to sample the GPUs")
		pidGlob    = flag.String("replica-pids", "run/replica-*.pid", "glob of the fleet's pid files, used to tell our own processes from foreign ones")

		logLevel = flag.String("log-level", "info", "log level: debug, info, warn or error")
	)
	flag.Parse()

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("bench: unknown log level %q", *logLevel)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	concurrencies, err := bench.ParseLevels(*levels)
	if err != nil {
		return err
	}
	// No default for the model: it is pinned engine configuration and
	// ops/versions.env is its single source of truth. A default here would be a
	// second copy, and drift between them turns every request into a 4xx that
	// the driver books as a replica failure.
	if *model == "" {
		return fmt.Errorf("bench: -model is required: pass \"$(ops/fleet.sh env MODEL)\", or run via make bench which does it for you")
	}

	// The same spec string the router takes, parsed by the same code, so the
	// harness cannot be checking a different fleet from the one being driven.
	replicas, err := fleet.ParseSpecs(strings.Split(*replicaSpecs, ","))
	if err != nil {
		return err
	}
	bases := make([]string, 0, len(replicas))
	for _, r := range replicas {
		bases = append(bases, r.BaseURL)
	}
	if len(bases) == 0 {
		log.Warn("no -replicas given: the sweep will not check the fleet is up before it starts")
	}

	contamination := bench.ContaminationConfig{Interval: *interval}
	if *sampleGPUs {
		if *gpus <= 0 {
			return fmt.Errorf("bench: -gpus is required when sampling: pass \"$(ops/fleet.sh env REPLICA_COUNT)\", or run via make bench which does it for you")
		}
		contamination.Prober = gpu.New()
		contamination.GPUs = gpu.Indexes(*gpus)
		if contamination.OwnPIDs, err = replicaPIDs(*pidGlob); err != nil {
			return err
		}
		if len(contamination.OwnPIDs) == 0 {
			// Without the fleet's own pids every replica would be classified as
			// a foreign process and every cell would be unclean, which is worse
			// than not sampling: it looks like evidence.
			return fmt.Errorf("bench: no replica pid files matched %q. Start the fleet with ops/fleet.sh up, or pass -sample-gpus=false to run without contamination evidence", *pidGlob)
		}
		log.Info("sampling GPUs for contamination", "gpus", *gpus, "replica_pids", contamination.OwnPIDs, "interval", *interval)
	} else {
		log.Warn("GPU sampling is off: every cell will record that its cleanliness is unproven")
	}

	// SIGINT abandons the cell in flight rather than waiting out its duration.
	// That cell is not cached — its rows stay under a .partial name and no cell
	// record is written — so the next pass re-runs it and keeps every cell that
	// did finish.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cells, sweepErr := bench.RunSweep(ctx, bench.SweepConfig{
		Dir:                  *dir,
		Target:               *target,
		Policy:               *policyName,
		Replicas:             bases,
		Concurrencies:        concurrencies,
		Repetitions:          *repetitions,
		CellDuration:         *duration,
		Warmup:               *warmup,
		FleetWarmup:          *fleetWarmup,
		Settle:               *settle,
		SLO:                  bench.SLO{TTFT: *sloTTFT, ITL: *sloITL},
		FailureThreshold:     *failureAt,
		WarmupDriftThreshold: *driftAt,
		Workload: bench.NewFixedWorkload(bench.FixedWorkload{
			Model:        *model,
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

// replicaPIDs reads the fleet's supervisor pids off its pid files. The process
// holding a card is the engine child of one of these, so ownership is resolved
// by ancestry rather than by matching these directly.
func replicaPIDs(glob string) ([]int, error) {
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("bench: bad pid file glob %q: %w", glob, err)
	}
	var pids []int
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("bench: read %s: %w", path, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err != nil {
			return nil, fmt.Errorf("bench: %s does not hold a pid: %w", path, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
