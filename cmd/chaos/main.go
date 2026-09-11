// Command chaos takes one replica away from a running fleet under steady
// open-loop load, brings it back, and records the recovery (#19, idea.md §7):
// every request's row, what the router did with the replica and when, the
// recovery curve, and a report whose drop count never stands without the
// reroute count beside it.
//
//	chaos -router http://127.0.0.1:8080 -dir runs/chaos/kill-session_affinity \
//	      -policy session_affinity -replica replica-2 -fault kill \
//	      -stop-cmd "ops/replica.sh kill 2" -start-cmd "ops/replica.sh up 2" \
//	      -arrival-rate 8 -slo-from runs/characterization \
//	      -model "$(ops/fleet.sh env MODEL)" -gpu-indexes "$(ops/fleet.sh env REPLICA_GPUS)" \
//	      -workload multiturn -sessions 307 ...
//
// It runs on the fleet host, because it kills and restarts a replica through
// the commands it is handed. `make chaos` hands it ops/replica.sh's, and
// ops/chaos.sh says what the operator does between the two policies' runs.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
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
		target     = flag.String("router", "http://127.0.0.1:8080", "the router to drive")
		dir        = flag.String("dir", "", "where the run's rows, report and run record land; one directory per run")
		policyName = flag.String("policy", "", "the policy the router is running; checked against the router before any load is offered")
		replica    = flag.String("replica", "", "the replica to take away, by the id the router knows it by")
		faultName  = flag.String("fault", string(bench.FaultKill), "kill: SIGKILL the replica with requests in flight. drain: drain it through the router, then stop it")
		stopCmd    = flag.String("stop-cmd", "", "shell command that takes the replica's process away: ops/replica.sh kill N for a kill, ops/replica.sh down N for a drain")
		startCmd   = flag.String("start-cmd", "", "shell command that starts the replica again and returns once it answers: ops/replica.sh up N")

		rate         = flag.Float64("arrival-rate", 0, "requests per second offered open-loop for the whole run; it has to sit where the whole fleet holds goodput, or there is no baseline to recover to")
		thinkTime    = flag.Duration("think-time", bench.DefaultThinkTime, "how long a session waits between its turns")
		duration     = flag.Duration("duration", 300*time.Second, "how long arrivals are offered for")
		warmup       = flag.Duration("warmup", 50*time.Second, "the start of the run excluded from every figure")
		faultAt      = flag.Duration("fault-at", 100*time.Second, "when, into the run, the replica is taken away")
		recoverAt    = flag.Duration("recover-at", 160*time.Second, "when, into the run, the replica is started again")
		bucket       = flag.Duration("bucket", bench.DefaultChaosBucket, "the recovery curve's resolution; the four times above must be whole multiples of it")
		tolerance    = flag.Float64("tolerance", bench.DefaultRecoveryTolerance, "how close to its baseline goodput has to come back to count as recovered")
		drainTimeout = flag.Duration("drain-timeout", bench.DefaultDrainTimeout, "how long a drain may take before the replica is stopped regardless")

		sloFrom = flag.String("slo-from", "", "a characterization directory or record to take the derived SLO from")
		sloTTFT = flag.Duration("slo-ttft", 0, "TTFT threshold, when not taken from a characterization")
		sloITL  = flag.Duration("slo-itl", 0, "inter-token latency threshold, when not taken from a characterization")

		// The workload flags cmd/bench takes, with its meanings, so that the
		// Makefile's WORKLOAD_ARGS names the same bytes to both commands.
		model        = flag.String("model", "", "the model the replicas serve; no default, it is pinned in ops/versions.env")
		workload     = flag.String("workload", "multiturn", "what to offer: fixed or multiturn")
		promptBytes  = flag.Int("prompt-bytes", 2048, "approximate prompt size per request, for the fixed workload")
		outputTokens = flag.Int("output-tokens", 64, "max_tokens per request, and the size of the reply written back into a multi-turn history")
		seed         = flag.Uint64("seed", 1, "workload seed; the same seed sends the same bytes")
		sessions     = flag.Int("sessions", 0, "size of the multi-turn session pool")
		workingSet   = flag.Float64("working-set", 0, "WS point to offer, as session tokens over aggregate fleet KV; needs -kv-capacity")
		kvCapacity   = flag.Int("kv-capacity", 0, "measured aggregate fleet KV in tokens")
		turns        = flag.Int("turns-per-session", 0, "turns per multi-turn session")
		promptTokens = flag.Int("prompt-tokens", 0, "new user text each multi-turn turn contributes")
		skew         = flag.Float64("skew", 0, "Zipf skew over the session pool")
		sharedSystem = flag.Float64("shared-system-prompt", 0, "fraction of sessions carrying the shared system prompt")
		branching    = flag.Float64("branching", 0, "fraction of sessions descending from a common ancestor")

		sampleGPUs = flag.Bool("sample-gpus", true, "sample nvidia-smi through the run for contamination evidence")
		gpus       = flag.String("gpu-indexes", "", "the cards the fleet uses, e.g. \"0,1,2,4,5\"; it is REPLICA_GPUS in ops/versions.env")
		interval   = flag.Duration("sample-interval", bench.DefaultSampleInterval, "how often to sample the GPUs")
		pidGlob    = flag.String("replica-pids", "run/replica-*.pid", "glob of the fleet's pid files, re-read at every sample because the run restarts a replica")

		logLevel = flag.String("log-level", "info", "log level: debug, info, warn or error")
	)
	flag.Parse()

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("chaos: unknown log level %q", *logLevel)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	fault, err := bench.ParseFault(*faultName)
	if err != nil {
		return err
	}
	if *stopCmd == "" || *startCmd == "" {
		return fmt.Errorf("chaos: -stop-cmd and -start-cmd are required: the run takes the replica away and brings it back through them")
	}
	if *model == "" {
		return fmt.Errorf("chaos: -model is required: pass \"$(ops/fleet.sh env MODEL)\", or run via make chaos which does it for you")
	}
	slo, err := resolveSLO(log, *sloFrom, bench.SLO{TTFT: *sloTTFT, ITL: *sloITL})
	if err != nil {
		return err
	}
	offered, err := buildWorkload(*workload, *model, *outputTokens, *seed, *promptBytes, bench.MultiTurnWorkload{
		Model: *model, Sessions: *sessions, WorkingSet: *workingSet, CapacityTokens: *kvCapacity,
		TurnsPerSession: *turns, PromptTokens: *promptTokens, OutputTokens: *outputTokens, Skew: *skew,
		SystemPromptFraction: *sharedSystem, BranchFraction: *branching, Seed: *seed,
	})
	if err != nil {
		return err
	}
	contamination, err := contaminationConfig(log, *sampleGPUs, *gpus, *interval, *pidGlob)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := bench.RunChaos(ctx, bench.ChaosConfig{
		Dir:           *dir,
		Router:        *target,
		Policy:        *policyName,
		Replica:       *replica,
		Fault:         fault,
		ArrivalRate:   *rate,
		ThinkTime:     *thinkTime,
		Duration:      *duration,
		Warmup:        *warmup,
		FaultAt:       *faultAt,
		RecoverAt:     *recoverAt,
		Bucket:        *bucket,
		Tolerance:     *tolerance,
		SLO:           slo,
		Workload:      offered,
		Stop:          shell(*stopCmd),
		Start:         shell(*startCmd),
		DrainTimeout:  *drainTimeout,
		Contamination: contamination,
		Log:           log,
	})
	if err != nil {
		return err
	}
	fmt.Print(s.Report())
	return nil
}

// shell runs an operator's command for the run, with its output on the run's
// own stderr so a failed kill or restart is visible where the run is watched.
func shell(command string) func(context.Context) error {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%q: %w", command, err)
		}
		return nil
	}
}

// resolveSLO takes the SLO from a characterization or from the flags, never
// both, and refuses one whose derivation the characterization itself doubts —
// the same rules cmd/bench applies.
func resolveSLO(log *slog.Logger, from string, stated bench.SLO) (bench.SLO, error) {
	if from == "" {
		return stated, nil
	}
	if stated.Applied() {
		return bench.SLO{}, fmt.Errorf("chaos: -slo-from and -slo-ttft/-slo-itl both given: the SLO comes from the characterization or from the flags, not from both")
	}
	c, err := characterize.Load(from)
	if err != nil {
		return bench.SLO{}, err
	}
	if ok, why := c.SLOUsable(); !ok {
		return bench.SLO{}, fmt.Errorf("chaos: the SLO in the characterization at %s must not be applied: %s. "+
			"The SLO is a stated multiple of that floor, so this run's goodput would inherit the problem. Re-run the characterization — "+
			"and if its floor read the replicas' prefix cache instead of prefilling, bring the fleet down first, because a floor measured against prompts the replicas already hold is not a floor (ADR-0004)",
			from, why)
	}
	log.Info("SLO read from the characterization rather than retyped", "record", from, "slo", c.SLO.String())
	return c.SLO.SLO(), nil
}

func buildWorkload(kind, model string, outputTokens int, seed uint64, promptBytes int, multiturn bench.MultiTurnWorkload) (bench.Workload, error) {
	switch kind {
	case "fixed":
		return bench.NewFixedWorkload(bench.FixedWorkload{Model: model, PromptBytes: promptBytes, OutputTokens: outputTokens, Seed: seed}), nil
	case "multiturn":
		return bench.NewMultiTurn(multiturn)
	}
	return nil, fmt.Errorf("chaos: unknown -workload %q; it is fixed or multiturn", kind)
}

// contaminationConfig is the GPU sampling the run carries its cleanliness
// evidence from. The fleet's pids are re-read at every sample, because the run
// restarts a replica and its new supervisor would otherwise be a stranger.
func contaminationConfig(log *slog.Logger, sample bool, gpuIndexes string, interval time.Duration, pidGlob string) (bench.ContaminationConfig, error) {
	cfg := bench.ContaminationConfig{Interval: interval}
	if !sample {
		log.Warn("GPU sampling is off: the run will record that its cleanliness is unproven")
		return cfg, nil
	}
	if gpuIndexes == "" {
		return cfg, fmt.Errorf("chaos: -gpu-indexes is required when sampling: pass \"$(ops/fleet.sh env REPLICA_GPUS)\", or run via make chaos")
	}
	indexes, err := gpu.ParseIndexes(gpuIndexes)
	if err != nil {
		return cfg, err
	}
	start, err := replicaPIDs(pidGlob)
	if err != nil {
		return cfg, err
	}
	if len(start) == 0 {
		return cfg, fmt.Errorf("chaos: no replica pid files matched %q, so every replica would be classified as foreign. Start the fleet with ops/fleet.sh up, or pass -sample-gpus=false", pidGlob)
	}
	cfg.Prober, cfg.GPUs, cfg.OwnPIDs = gpu.New(), indexes, start
	cfg.OwnPIDsNow = func() []int {
		now, err := replicaPIDs(pidGlob)
		if err != nil || len(now) == 0 {
			// Between a kill and a restart the pid files are being rewritten; the
			// pids known at the start are the best answer for that instant.
			return start
		}
		return now
	}
	return cfg, nil
}

// replicaPIDs reads the fleet's supervisor pids off its pid files.
func replicaPIDs(glob string) ([]int, error) {
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("chaos: bad pid file glob %q: %w", glob, err)
	}
	var pids []int
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			// A replica being killed removes its pid file between the glob and
			// the read.
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err != nil {
			return nil, fmt.Errorf("chaos: %s does not hold a pid: %w", path, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
