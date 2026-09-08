// Command bench drives a running fleet along one of two load axes.
//
// By default it holds a fixed number of virtual users at each of eight
// concurrency levels — the closed-loop axis, where offered load is an outcome.
// With -driver open_loop it instead fires on a fixed schedule at each arrival
// rate, which is the open-loop axis and where the headline goodput number comes
// from: a closed-loop driver throttles itself exactly at the saturation that
// number is about.
//
// Either way it records one row per request, samples the GPUs throughout so
// every cell carries its own cleanliness evidence, and compacts the run to
// Parquet at the end.
//
//	bench -router http://127.0.0.1:8080 \
//	      -dir runs/concurrency \
//	      -policy round_robin \
//	      -cell-duration 60s -repetitions 3
//
//	bench -router http://127.0.0.1:8080 \
//	      -dir runs/goodput -driver open_loop \
//	      -policy round_robin -slo-ttft 990ms -slo-itl 24ms
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
	"github.com/yuchia329/kvroute/internal/characterize"
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
		driver       = flag.String("driver", string(bench.ClosedLoopDriver), "which axis to run: closed_loop holds virtual users at each -concurrency level, open_loop fires at each -arrival-rates level, both runs the two in one directory")
		levels       = flag.String("concurrency", bench.FormatLevels(bench.ConcurrencySweep), "closed-loop axis: concurrency levels to sweep, holding that many virtual users")
		rates        = flag.String("arrival-rates", bench.FormatRates(bench.ArrivalRateSweep), "open-loop axis: arrival rates in requests per second, fired on a fixed schedule whether or not earlier requests have finished")
		thinkTime    = flag.Duration("think-time", bench.DefaultThinkTime, "open-loop axis: how long a session waits between its turns. With the arrival rate this sets how many conversations a cell holds open at once — rate x think time, by Little's law — and so whether sessions reach their later turns at all. Ignored by the closed-loop axis, where a session's next turn goes out when its last response arrives")
		repetitions  = flag.Int("repetitions", 3, "repetitions per level; p99 is noisy at low sample counts")
		duration     = flag.Duration("cell-duration", 60*time.Second, "how long each cell keeps starting new turns")
		warmup       = flag.Duration("warmup", 10*time.Second, "slice at the start of each cell whose rows are recorded but excluded from the summary; a duration, so every load level forfeits the same share of its window")
		fleetWarmup  = flag.Int("fleet-warmup", 3, "requests sent to each replica directly before the first cell, so every replica has done a real forward pass")
		settle       = flag.Duration("settle", 10*time.Second, "pause between cells, so one cell's tail stays out of the next one's window")

		sloFrom   = flag.String("slo-from", "", "a characterization directory or record to take the derived SLO from, instead of stating it as the two flags below; the SLO is derived from a measured floor, and a threshold retyped by hand is one that can differ from the one that was derived")
		sloTTFT   = flag.Duration("slo-ttft", 0, "TTFT threshold; unset means no SLO was applied and goodput is not reported")
		sloITL    = flag.Duration("slo-itl", 0, "inter-token latency threshold, evaluated against each request's median gap")
		failureAt = flag.Float64("failure-threshold", bench.DefaultFailureThreshold, "failure rate above which a cell is flagged")
		driftAt   = flag.Float64("warmup-drift-threshold", bench.DefaultWarmupDriftThreshold, "how much slower a cell's first measured half may be than its second before it is flagged as under-warmed; negative disables the check")
		lagAt     = flag.Duration("schedule-lag-threshold", bench.DefaultScheduleLagThreshold, "how late an open-loop cell's requests may be sent against the schedule that asked for them, at p99, before the cell is flagged as not having offered the rate it reports; negative disables the check")

		model        = flag.String("model", "", "the model the replicas serve; no default, it is pinned in ops/versions.env")
		outputTokens = flag.Int("output-tokens", 64, "max_tokens per request, and the size of the reply written back into a multi-turn history")
		seed         = flag.Uint64("seed", 1, "workload seed; the same seed sends the same bytes")

		workload    = flag.String("workload", workloadFixed, "what to offer: "+workloadFixed+", independent single-turn requests sharing no prefix, or "+workloadMultiTurn+", the multi-turn generator with both pressure knobs")
		promptBytes = flag.Int("prompt-bytes", 2048, "approximate prompt size per request, for the fixed workload")

		sessions     = flag.Int("sessions", 0, "size of the multi-turn session pool; leave it zero and pass -working-set to derive it from measured capacity instead")
		workingSet   = flag.Float64("working-set", 0, "WS point to offer: session tokens over aggregate fleet KV. Needs -kv-capacity")
		kvCapacity   = flag.Int("kv-capacity", 0, "measured aggregate fleet KV in tokens, read from the characterization gates' capacity record rather than estimated; every WS point moves with this denominator, and the by-hand estimate was 9.8% low")
		turns        = flag.Int("turns-per-session", 0, "turns per multi-turn session; zero takes the generator's default")
		promptTokens = flag.Int("prompt-tokens", 0, "new user text each multi-turn turn contributes, on top of the history it resends; zero takes the generator's default")
		skew         = flag.Float64("skew", 0, "Zipf skew over the session pool: 0 is uniform, and concentration rises from there. The axis that creates load imbalance")
		sharedSystem = flag.Float64("shared-system-prompt", 0, "fraction of sessions carrying the shared system prompt")
		branching    = flag.Float64("branching", 0, "fraction of sessions descending from a common ancestor rather than opening on their own content")

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
	var err error

	// Which axis runs is a choice rather than an inference from which list was
	// passed: both lists carry the package's own ladder as their default, so
	// running one axis must not silently run the other's eight cells too. On a
	// shared box that would be hours.
	var concurrencies []int
	var arrivalRates []float64
	switch bench.Driver(*driver) {
	case bench.ClosedLoopDriver:
		if concurrencies, err = bench.ParseLevels(*levels); err != nil {
			return err
		}
	case bench.OpenLoopDriver:
		if arrivalRates, err = bench.ParseRates(*rates); err != nil {
			return err
		}
	case "both":
		if concurrencies, err = bench.ParseLevels(*levels); err != nil {
			return err
		}
		if arrivalRates, err = bench.ParseRates(*rates); err != nil {
			return err
		}
	default:
		return fmt.Errorf("bench: unknown -driver %q: want %s, %s or both", *driver, bench.ClosedLoopDriver, bench.OpenLoopDriver)
	}
	// No default for the model: it is pinned engine configuration and
	// ops/versions.env is its single source of truth. A default here would be a
	// second copy, and drift between them turns every request into a 4xx that
	// the driver books as a replica failure.
	if *model == "" {
		return fmt.Errorf("bench: -model is required: pass \"$(ops/fleet.sh env MODEL)\", or run via make bench which does it for you")
	}

	slo := bench.SLO{TTFT: *sloTTFT, ITL: *sloITL}
	if *sloFrom != "" {
		if slo.Applied() {
			// Two sources for one threshold is how a table comes to name an SLO it
			// was not judged against.
			return fmt.Errorf("bench: -slo-from and -slo-ttft/-slo-itl both given: the SLO comes from the characterization or from the flags, not from both")
		}
		c, err := characterize.Load(*sloFrom)
		if err != nil {
			return err
		}
		if ok, why := c.SLOUsable(); !ok {
			// Every cell would be judged against this threshold and none of them
			// would record that its derivation was in doubt.
			return fmt.Errorf("bench: the SLO in the characterization at %s must not be applied: %s. "+
				"The SLO is a stated multiple of that floor, so every cell of this sweep would inherit the problem. Re-run the characterization — "+
				"and if its floor read the replicas' prefix cache instead of prefilling, bring the fleet down first, because a floor measured against prompts the replicas already hold is not a floor (ADR-0004)",
				*sloFrom, why)
		}
		slo = c.SLO.SLO()
		log.Info("SLO read from the characterization rather than retyped",
			"record", *sloFrom, "slo", c.SLO.String(), "measured_at", c.At)

		// The floor is sound or the run would have stopped above. What the record
		// says about the rest of the fleet is still the operator's to know, and the
		// symmetry verdict most of all: a policy difference measured on a fleet
		// whose replicas are not interchangeable could be the host rather than the
		// policy.
		if !c.Symmetry.Symmetric {
			log.Warn("the characterization does not find the replicas interchangeable, so a policy difference in this sweep could be host asymmetry rather than the policy",
				"record", *sloFrom)
		}
		for _, reason := range c.FlagReasons {
			log.Warn("the characterization this SLO came from is flagged", "reason", reason)
		}
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

	offered, err := buildWorkload(workloadConfig{
		kind: *workload, model: *model, outputTokens: *outputTokens, seed: *seed,
		promptBytes:  *promptBytes,
		sessions:     *sessions,
		workingSet:   *workingSet,
		kvCapacity:   *kvCapacity,
		turns:        *turns,
		promptTokens: *promptTokens,
		skew:         *skew,
		sharedSystem: *sharedSystem,
		branching:    *branching,
	})
	if err != nil {
		return err
	}
	log.Info("offering", "workload", offered.Name())

	cells, sweepErr := bench.RunSweep(ctx, bench.SweepConfig{
		Dir:                  *dir,
		Target:               *target,
		Policy:               *policyName,
		Replicas:             bases,
		Concurrencies:        concurrencies,
		ArrivalRates:         arrivalRates,
		Repetitions:          *repetitions,
		CellDuration:         *duration,
		Warmup:               *warmup,
		FleetWarmup:          *fleetWarmup,
		Settle:               *settle,
		ThinkTime:            *thinkTime,
		SLO:                  slo,
		FailureThreshold:     *failureAt,
		WarmupDriftThreshold: *driftAt,
		ScheduleLagThreshold: *lagAt,
		Workload:             offered,
		Contamination:        contamination,
		Log:                  log,
	})

	// Report and compact whatever finished, even if the sweep stopped early:
	// the cells that ran are still cells, and they are what the next pass
	// resumes from.
	if len(cells) > 0 {
		table := bench.Table(cells)
		fmt.Print("\n" + table)
		path := filepath.Join(*dir, "results.md")
		if err := os.WriteFile(path, []byte(header(*policyName, cells, slo)+table), 0o644); err != nil {
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

// The two workloads the sweep can offer.
const (
	workloadFixed     = "fixed"
	workloadMultiTurn = "multiturn"
)

type workloadConfig struct {
	kind         string
	model        string
	outputTokens int
	seed         uint64

	promptBytes int

	sessions     int
	workingSet   float64
	kvCapacity   int
	turns        int
	promptTokens int
	skew         float64
	sharedSystem float64
	branching    float64
}

// buildWorkload settles what the sweep offers.
//
// It runs before the first cell rather than inside the sweep, because a
// configuration the generator refuses is worth refusing before the fleet has
// been warmed and the first hour of GPU time spent.
func buildWorkload(cfg workloadConfig) (bench.Workload, error) {
	switch cfg.kind {
	case workloadFixed:
		return bench.NewFixedWorkload(bench.FixedWorkload{
			Model:        cfg.model,
			PromptBytes:  cfg.promptBytes,
			OutputTokens: cfg.outputTokens,
			Seed:         cfg.seed,
		}), nil
	case workloadMultiTurn:
		return bench.NewMultiTurn(bench.MultiTurnWorkload{
			Model:                cfg.model,
			Sessions:             cfg.sessions,
			WorkingSet:           cfg.workingSet,
			CapacityTokens:       cfg.kvCapacity,
			TurnsPerSession:      cfg.turns,
			PromptTokens:         cfg.promptTokens,
			OutputTokens:         cfg.outputTokens,
			Skew:                 cfg.skew,
			SystemPromptFraction: cfg.sharedSystem,
			BranchFraction:       cfg.branching,
			Seed:                 cfg.seed,
		})
	}
	return nil, fmt.Errorf("bench: unknown -workload %q; it is %s or %s", cfg.kind, workloadFixed, workloadMultiTurn)
}

// header states what produced the table, because a latency table without its
// driver and its SLO is not interpretable.
//
// The driver is read off the cells rather than assumed: this command runs
// either axis, and a header that named a driver the run did not use would be
// worse than none.
func header(policy string, cells []bench.Cell, slo bench.SLO) string {
	stated := "none applied — goodput is not reported and SLO violations were not counted"
	if slo.Applied() {
		stated = fmt.Sprintf("TTFT < %v, inter-token p50 < %v", slo.TTFT, slo.ITL)
	}
	title, driverNote := tableHeading(cells)
	return fmt.Sprintf("# %s — %s\n\n%s\n\nSLO: %s\n\n", title, policy, driverNote, stated)
}

// closedLoopNote and openLoopNote are why a reader has to know which drove the
// table before reading a single number in it.
const (
	closedLoopNote = "Driver: closed-loop (offered load is an outcome, so the tail here is\noptimistic; the headline goodput number comes from the open-loop driver)."
	openLoopNote   = "Driver: open-loop (offered load is an input and is held when the fleet\nslows, so these numbers describe behaviour at and past saturation)."
)

// tableHeading names the table and states the driver behind it.
func tableHeading(cells []bench.Cell) (title, driverNote string) {
	closed, open := false, false
	for _, c := range cells {
		switch c.Driver {
		case bench.OpenLoopDriver:
			open = true
		default:
			closed = true
		}
	}
	switch {
	case open && closed:
		return "Load sweep", "Drivers: closed-loop and open-loop. Each row says which produced it, and\nrows from the two are never compared without saying so: a closed-loop tail is\noptimistic by construction."
	case open:
		return "Arrival rate sweep", openLoopNote
	default:
		return "Concurrency sweep", closedLoopNote
	}
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
