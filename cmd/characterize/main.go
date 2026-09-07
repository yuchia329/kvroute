// Command characterize establishes the measured facts every downstream number
// depends on.
//
// It reads aggregate KV capacity off all six replicas, records the host's GPU
// topology, drives each replica on its own at two load levels, takes the
// hardware latency floor from the concurrency-1 rows and derives the SLO from
// it as a stated multiple, and reports whether the six replicas are
// interchangeable.
//
//	characterize -replicas "$(ops/fleet.sh replicas)" \
//	             -model "$(ops/fleet.sh env MODEL)" \
//	             -gpus "$(ops/fleet.sh env REPLICA_COUNT)" \
//	             -dir docs/measurements/characterization
//
// It drives replicas directly rather than through the router: every question it
// answers is about a replica, and the router's policy would be in the answer.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/gpu"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		replicaSpecs = flag.String("replicas", "", "the fleet to characterize, as the router's own -replicas spec; each is driven directly")
		dir          = flag.String("dir", "runs/characterization", "where the rows and the record are written")
		model        = flag.String("model", "", "the model the replicas serve; no default, it is pinned in ops/versions.env")

		levels        = flag.String("levels", "1,16", "load levels each replica is driven at, on its own; the first must be 1, which is where the floor lives")
		repetitions   = flag.Int("repetitions", 2, "how many times the whole pass runs; the replica order reverses on alternate passes so a drifting host cannot look like a slow replica")
		probeDuration = flag.Duration("probe-duration", 30*time.Second, "how long each replica is driven at each level")
		warmup        = flag.Duration("warmup", 5*time.Second, "slice at the start of each probe recorded but excluded from the summary")
		replicaWarmup = flag.Int("replica-warmup", 3, "requests sent to each replica directly before the first probe, so no replica serves its first forward pass inside the floor")
		settle        = flag.Duration("settle", 5*time.Second, "pause between probes")

		sloMultiple   = flag.Float64("slo-multiple", characterize.DefaultSLOMultiple, "the SLO is this many times the measured floor")
		tolerance     = flag.Float64("tolerance", characterize.DefaultSymmetryTolerance, "how far replicas may differ before the fleet is not interchangeable")
		sessionTokens = flag.Int("session-tokens", characterize.DefaultSessionTokens, "tokens per session, the divisor that turns fleet capacity into a resident session count")

		promptBytes  = flag.Int("prompt-bytes", 2048, "approximate prompt size per request")
		outputTokens = flag.Int("output-tokens", 64, "max_tokens per request")
		seed         = flag.Uint64("seed", 1, "workload seed; the same seed sends the same bytes")

		sampleGPUs = flag.Bool("sample-gpus", true, "read the host topology and sample nvidia-smi during each probe")
		gpus       = flag.Int("gpus", 0, "how many GPUs the fleet uses; no default, it is REPLICA_COUNT in ops/versions.env")
		interval   = flag.Duration("sample-interval", bench.DefaultSampleInterval, "how often to sample the GPUs")
		pidGlob    = flag.String("replica-pids", "run/replica-*.pid", "glob of the fleet's pid files, used to tell our own processes from foreign ones")

		render = flag.String("render", "", "re-render report.md from an existing run's characterization.json in this directory, without measuring anything")

		logLevel = flag.String("log-level", "info", "log level: debug, info, warn or error")
	)
	flag.Parse()

	// The report is a reading of the record, not the record itself, so it can
	// be rebuilt at any time — the same relationship ADR-0002 gives Parquet and
	// the JSONL rows. Without this, correcting a sentence in the report would
	// mean forty minutes of GPU time or a report that no longer matches the
	// code that claims to produce it.
	if *render != "" {
		return rerender(*render)
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("characterize: unknown log level %q", *logLevel)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if *model == "" {
		return fmt.Errorf("characterize: -model is required: pass \"$(ops/fleet.sh env MODEL)\", or run via make characterize which does it for you")
	}
	replicas, err := fleet.ParseSpecs(strings.Split(*replicaSpecs, ","))
	if err != nil {
		return err
	}
	if len(replicas) == 0 {
		return fmt.Errorf("characterize: -replicas is required: pass \"$(ops/fleet.sh replicas)\", or run via make characterize")
	}
	// Validated by the same code the router validates its fleet with, so a
	// duplicated id — which would count one replica's KV capacity twice in the
	// fleet aggregate — is rejected here rather than doubling a number
	// everything downstream scales off.
	if _, err := fleet.New(replicas); err != nil {
		return err
	}
	loadLevels, err := parseLevels(*levels)
	if err != nil {
		return err
	}
	// The floor is what the SLO is derived from and it only exists at
	// concurrency 1. A run without it would produce a symmetry verdict and an
	// SLO of zero, and the zero would look like a decision.
	if loadLevels[0] != 1 {
		return fmt.Errorf("characterize: the first level must be 1: that is where the hardware floor the SLO is derived from lives, got %d", loadLevels[0])
	}

	contamination := bench.ContaminationConfig{Interval: *interval}
	var prober *gpu.Prober
	if *sampleGPUs {
		if *gpus <= 0 {
			return fmt.Errorf("characterize: -gpus is required when sampling: pass \"$(ops/fleet.sh env REPLICA_COUNT)\", or run via make characterize")
		}
		prober = gpu.New()
		contamination.Prober = prober
		contamination.GPUs = gpu.Indexes(*gpus)
		if contamination.OwnPIDs, err = replicaPIDs(*pidGlob); err != nil {
			return err
		}
		if len(contamination.OwnPIDs) == 0 {
			return fmt.Errorf("characterize: no replica pid files matched %q. Start the fleet with ops/fleet.sh up, or pass -sample-gpus=false to run without contamination evidence", *pidGlob)
		}
	} else {
		log.Warn("GPU sampling is off: the topology will not be recorded and every probe will say its cleanliness is unproven")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c, runErr := characterize.Run(ctx, characterize.Config{
		Dir:           *dir,
		Replicas:      replicas,
		Model:         *model,
		Levels:        loadLevels,
		Repetitions:   *repetitions,
		ProbeDuration: *probeDuration,
		Warmup:        *warmup,
		ReplicaWarmup: *replicaWarmup,
		Settle:        *settle,
		SessionTokens: *sessionTokens,
		SLOMultiple:   *sloMultiple,
		Tolerance:     *tolerance,
		Workload: bench.NewFixedWorkload(bench.FixedWorkload{
			Model:        *model,
			PromptBytes:  *promptBytes,
			OutputTokens: *outputTokens,
			Seed:         *seed,
		}),
		Prober:        prober,
		Contamination: contamination,
		Log:           log,
	})
	if runErr != nil {
		return runErr
	}

	report := characterize.Report(c)
	path := filepath.Join(*dir, "report.md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		return err
	}
	fmt.Print("\n" + report)

	// The SLO exists to be applied, and the sweep takes it as two flags. Printed
	// rather than left to be transcribed off a table, because a threshold
	// retyped by hand is a threshold that can differ from the one that was
	// derived.
	fmt.Printf("Derived SLO, for the sweep:\n\n    make bench BENCH_ARGS=\"-slo-ttft %v -slo-itl %v\"\n\n", c.SLO.TTFT, c.SLO.ITL)
	log.Info("characterization complete", "report", path,
		"record", filepath.Join(*dir, "characterization.json"),
		"symmetric", c.Symmetry.Symmetric, "flagged", c.Flagged)
	return nil
}

// rerender rewrites a run's report from the record it already wrote.
func rerender(dir string) error {
	path := filepath.Join(dir, "characterization.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("characterize: %w", err)
	}
	var c characterize.Characterization
	if err := json.Unmarshal(contents, &c); err != nil {
		return fmt.Errorf("characterize: %s is not a characterization record: %w", path, err)
	}
	report := filepath.Join(dir, "report.md")
	if err := os.WriteFile(report, []byte(characterize.Report(c)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "characterize: re-rendered %s from %s\n", report, path)
	return nil
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
			return nil, fmt.Errorf("characterize: load level %q is not a positive integer", field)
		}
		levels = append(levels, n)
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("characterize: no load levels given")
	}
	return levels, nil
}

// replicaPIDs reads the fleet's supervisor pids off its pid files. The process
// holding a card is the engine child of one of these, so ownership is resolved
// by ancestry rather than by matching these directly.
func replicaPIDs(glob string) ([]int, error) {
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("characterize: bad pid file glob %q: %w", glob, err)
	}
	var pids []int
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("characterize: read %s: %w", path, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err != nil {
			return nil, fmt.Errorf("characterize: %s does not hold a pid: %w", path, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
