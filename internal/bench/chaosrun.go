package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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

// Defaults for a chaos run's shape, when its configuration leaves them unset.
const (
	// DefaultChaosBucket is the width of a recovery curve's buckets. Five seconds
	// at the rates the fleet is driven at is some tens of requests a bucket:
	// enough that one bucket's goodput is not one request's luck, and fine enough
	// that a dip lasting one restart is several buckets deep.
	DefaultChaosBucket = 5 * time.Second
	// DefaultRecoveryTolerance is how close to its baseline goodput has to come
	// back to count as recovered.
	DefaultRecoveryTolerance = 0.10
	// DefaultDrainTimeout bounds how long a drain may take before the replica is
	// stopped regardless, with whatever it still holds dropped and counted.
	DefaultDrainTimeout = 2 * time.Minute
)

// chaosPollInterval is how often a run reads the router's view of the replica it
// took away, and so the resolution of every event the router decides.
const chaosPollInterval = 100 * time.Millisecond

// ChaosRows is the name a chaos run's per-request rows are written under.
const ChaosRows = "rows.jsonl"

// ParseFault reads a fault named on a command line.
func ParseFault(name string) (Fault, error) {
	switch f := Fault(name); f {
	case FaultKill, FaultDrain:
		return f, nil
	}
	return "", fmt.Errorf("bench: unknown fault %q: want %s or %s", name, FaultKill, FaultDrain)
}

// chaosBlock is where in the workload's user space chaos runs send from: far
// above every block the sweeps and the pressure grid use, the largest of which is
// under a million, so a chaos run never re-sends a cell's conversations and no
// cell re-sends a chaos run's (ADR-0004).
const chaosBlock = 1 << 30

// ChaosWorkloadOffset is the slice of the workload's user space a chaos run
// sends from. Derived from the fault and the rate and deliberately not from the
// policy, so the two policies' runs of one scenario send identical bytes — which
// is what lets their curves be compared at all — while a kill and a drain do not
// share a slice.
func ChaosWorkloadOffset(fault Fault, rate float64) int {
	kind := 0
	if fault == FaultDrain {
		kind = 1
	}
	return (chaosBlock + kind*LoadLevelsPerDriver + int(math.Round(rate))) * WorkloadStride
}

// ChaosConfig configures one chaos run: a replica taken away under steady
// open-loop load and brought back.
type ChaosConfig struct {
	// Dir is where the run's rows, report and run record land.
	Dir string
	// Router is the router's base URL. The run drains and restores through it,
	// and reads the replica's place in rotation off its stats.
	Router string
	// Policy is the policy the router is running. The run is told it rather than
	// setting it, as a sweep is, and refuses a router running another.
	Policy string
	// Replica is the replica to take away, by the id the router knows it by.
	Replica string
	Fault   Fault

	ArrivalRate float64
	// ThinkTime is how long a session waits between its turns. Zero uses
	// DefaultThinkTime.
	ThinkTime time.Duration

	// Duration is how long arrivals are offered for; Warmup is the part of that
	// excluded from every figure; FaultAt and RecoverAt are when, into the run,
	// the replica is taken away and started again. All four are whole multiples
	// of Bucket, so the curve's buckets line up with the run: the fault falls on
	// a boundary rather than splitting a bucket, and the run ends at the end of
	// one rather than partway through.
	Duration  time.Duration
	Warmup    time.Duration
	FaultAt   time.Duration
	RecoverAt time.Duration
	// Bucket is the curve's resolution, and Tolerance how close to its baseline
	// goodput has to return to count as recovered. Zero uses the defaults.
	Bucket    time.Duration
	Tolerance float64
	// SLO is what goodput is judged against, and is required: a recovery curve
	// with no SLO has nothing to plot.
	SLO      SLO
	Workload Workload

	// Stop takes the replica's process away: it kills it for FaultKill, and stops
	// it for FaultDrain once the router has drained it. Start brings it back and
	// returns once it answers. The run owns neither process, so both are handed
	// in; cmd/chaos wires them to ops/replica.sh.
	Stop  func(context.Context) error
	Start func(context.Context) error
	// DrainTimeout bounds how long a drain may take. Zero uses
	// DefaultDrainTimeout.
	DrainTimeout time.Duration

	Contamination ContaminationConfig
	Log           *slog.Logger
}

// withDefaults fills in the fields that have one.
func (cfg ChaosConfig) withDefaults() ChaosConfig {
	if cfg.Bucket <= 0 {
		cfg.Bucket = DefaultChaosBucket
	}
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = DefaultRecoveryTolerance
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = DefaultDrainTimeout
	}
	if cfg.ThinkTime <= 0 {
		cfg.ThinkTime = DefaultThinkTime
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return cfg
}

// validate refuses a run that could not measure what it says it measures.
func (cfg ChaosConfig) validate() error {
	switch {
	case cfg.Dir == "":
		return errors.New("bench: a chaos run needs a directory to write to")
	case cfg.Router == "":
		return errors.New("bench: a chaos run needs a router to drive")
	case cfg.Policy == "":
		return errors.New("bench: the policy the router is running must be named")
	case cfg.Replica == "":
		return errors.New("bench: a chaos run needs a replica to take away")
	case cfg.ArrivalRate <= 0:
		return fmt.Errorf("bench: a chaos run needs an arrival rate, got %g", cfg.ArrivalRate)
	case cfg.ArrivalRate != math.Trunc(cfg.ArrivalRate) || cfg.ArrivalRate >= LoadLevelsPerDriver:
		return fmt.Errorf("bench: a chaos run's rate must be a whole number of requests per second below %d, got %g: "+
			"its slice of the workload's user space is keyed on the rate, and any other would share another run's prompts (ADR-0004)",
			LoadLevelsPerDriver, cfg.ArrivalRate)
	case !cfg.SLO.Applied():
		return errors.New("bench: a chaos run needs an SLO: goodput is defined by it, and a recovery curve without one has nothing to plot")
	case cfg.Workload == nil:
		return errors.New("bench: a chaos run needs a workload")
	case cfg.Stop == nil || cfg.Start == nil:
		return errors.New("bench: a chaos run needs a way to stop the replica and a way to start it again")
	}
	if _, err := ParseFault(string(cfg.Fault)); err != nil {
		return err
	}
	if !(cfg.Warmup >= 0 && cfg.Warmup < cfg.FaultAt && cfg.FaultAt < cfg.RecoverAt && cfg.RecoverAt < cfg.Duration) {
		return fmt.Errorf("bench: a chaos run's times must run warm-up < fault < recovery < end, got %v, %v, %v and %v",
			cfg.Warmup, cfg.FaultAt, cfg.RecoverAt, cfg.Duration)
	}
	for _, at := range []struct {
		name string
		d    time.Duration
	}{{"warm-up", cfg.Warmup}, {"fault", cfg.FaultAt}, {"recovery", cfg.RecoverAt}, {"duration", cfg.Duration}} {
		if at.d%cfg.Bucket != 0 {
			return fmt.Errorf("bench: the %s at %v is not a whole number of %v buckets, so the curve would not line up with the run: a bucket would straddle it", at.name, at.d, cfg.Bucket)
		}
	}
	// See Recovery: the bucket the fault comes at the end of is the fault's, so
	// the baseline needs at least one more before it.
	if cfg.FaultAt-cfg.Warmup < 2*cfg.Bucket {
		return fmt.Errorf("bench: a chaos run needs at least two %v buckets between its warm-up and its fault — one to measure the healthy fleet over, and the one the fault's in-flight requests land in — and has %v",
			cfg.Bucket, cfg.FaultAt-cfg.Warmup)
	}
	return nil
}

// RunChaos takes a replica away from a running fleet under steady open-loop
// load, brings it back, and records what happened: every request's row, what
// the router did with the replica and when, the recovery curve, and a report.
//
// Open-loop because the curve is goodput under a fixed offered load. A
// closed-loop driver slows down when the fleet does, so it would offer less
// exactly while the fleet was short a replica, and draw the dip shallower than
// it was.
//
// The fault and the recovery happen at fixed points of the run, so that two
// policies' runs of one scenario line up bucket for bucket. A run that is
// interrupted, or whose fault or recovery could not be carried out, returns an
// error and writes no run record: its rows are kept, and nothing will read them as
// a result.
func RunChaos(ctx context.Context, cfg ChaosConfig) (ChaosRun, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return ChaosRun{}, err
	}
	if _, err := LoadChaosRun(cfg.Dir); err == nil {
		return ChaosRun{}, fmt.Errorf("bench: %s already holds a finished chaos run; a second into the same directory would overwrite the first, so give it its own", cfg.Dir)
	}
	stats, err := cfg.checkRouter(ctx)
	if err != nil {
		return ChaosRun{}, err
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return ChaosRun{}, fmt.Errorf("bench: create %s: %w", cfg.Dir, err)
	}
	// Rows stream under a partial name and are renamed into place only once the
	// run has finished, as a cell's are, so a run cut off partway leaves readable
	// rows under a name that says so (ADR-0002). Partial rows an earlier attempt
	// left are moved aside rather than appended to: one file holding two runs'
	// rows would be neither run's record.
	rowsPath := filepath.Join(cfg.Dir, ChaosRows)
	partial := rowsPath + ".partial"
	if _, err := os.Stat(partial); err == nil {
		aside := partial + "-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(partial, aside); err != nil {
			return ChaosRun{}, fmt.Errorf("bench: move an earlier attempt's rows aside: %w", err)
		}
		cfg.Log.Warn("an earlier attempt at this run left partial rows; they are kept, moved aside", "rows", aside)
	}
	rows, err := record.Open[Result](partial)
	if err != nil {
		return ChaosRun{}, err
	}
	defer rows.Close()

	// The id seeds the arrival rotation, and it names no policy, so both
	// policies' runs offer the same conversations in the same order.
	id := fmt.Sprintf("chaos-%s-%s-a%s", cfg.Fault, cfg.Replica, strconv.FormatFloat(cfg.ArrivalRate, 'g', -1, 64))
	cfg.Log.Info("chaos run starting", "id", id, "policy", stats.Policy, "replica", cfg.Replica, "fault", cfg.Fault,
		"rate", cfg.ArrivalRate, "fault_at", cfg.FaultAt, "recover_at", cfg.RecoverAt, "duration", cfg.Duration)

	cfg.Contamination.Log = cfg.Log
	start := time.Now()
	watcher := Watch(ctx, cfg.Contamination)
	events := &eventLog{}
	polling, stopPolling := context.WithCancel(ctx)
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		cfg.watchRotation(polling, events)
	}()

	type driven struct {
		results []Result
		err     error
	}
	drive := make(chan driven, 1)
	go func() {
		results, err := RunOpenLoop(ctx, DriverConfig{
			Target:      cfg.Router,
			ArrivalRate: cfg.ArrivalRate,
			ThinkTime:   cfg.ThinkTime,
			Duration:    cfg.Duration,
			Warmup:      cfg.Warmup,
			Workload:    Shifted(cfg.Workload, ChaosWorkloadOffset(cfg.Fault, cfg.ArrivalRate)),
			Rows:        rows,
			Labels:      Labels{CellID: id, Policy: stats.Policy},
			Log:         cfg.Log,
		})
		drive <- driven{results, err}
	}()

	faultErr := cfg.injectAndRecover(ctx, start, events)
	run := <-drive
	stopPolling()
	<-polled
	contamination := watcher.Stop()
	if err := rows.Close(); err != nil && run.err == nil {
		run.err = err
	}
	switch {
	case run.err != nil:
		return ChaosRun{}, fmt.Errorf("bench: chaos run %s: %w", id, run.err)
	case ctx.Err() != nil:
		return ChaosRun{}, fmt.Errorf("bench: chaos run %s was interrupted, and is not a result: %w", id, ctx.Err())
	case faultErr != nil:
		return ChaosRun{}, fmt.Errorf("bench: chaos run %s did not happen as configured, and is not a result: %w", id, faultErr)
	}
	if err := os.Rename(partial, rowsPath); err != nil {
		return ChaosRun{}, fmt.Errorf("bench: chaos run %s: %w", id, err)
	}

	s := ChaosRun{
		ID:          stats.Policy + "-" + id,
		Policy:      stats.Policy,
		Replica:     cfg.Replica,
		Fault:       cfg.Fault,
		ArrivalRate: cfg.ArrivalRate,
		ThinkTimeNs: cfg.ThinkTime.Nanoseconds(),
		Workload:    cfg.Workload.Name(),
		SLO:         cfg.SLO,
		StartedAtNs: start.UnixNano(),
		DurationNs:  cfg.Duration.Nanoseconds(),
		WarmupNs:    cfg.Warmup.Nanoseconds(),
		FaultAtNs:   cfg.FaultAt.Nanoseconds(),
		RecoverAtNs: cfg.RecoverAt.Nanoseconds(),
		BucketNs:    cfg.Bucket.Nanoseconds(),
		Tolerance:   cfg.Tolerance,

		Contamination: contamination,
	}
	if stats.Spill != nil {
		s.KVHighWater, s.LoadImbalanceFactor = stats.Spill.KVHighWater, stats.Spill.LoadImbalanceFactor
	}
	s.Assess(run.results, events.relativeTo(start.Add(cfg.FaultAt)))

	// The report first and the run record last, so that the record's presence
	// means everything else is there too.
	if err := os.WriteFile(filepath.Join(cfg.Dir, "report.md"), []byte(s.Report()), 0o644); err != nil {
		return ChaosRun{}, fmt.Errorf("bench: write report: %w", err)
	}
	if err := SaveChaosRun(cfg.Dir, s); err != nil {
		return ChaosRun{}, err
	}
	cfg.Log.Info("chaos run complete", "id", s.ID, "requests", s.Summary.Requests,
		"rerouted", s.Summary.Rerouted, "dropped", s.Summary.Dropped, "deficit", s.Recovery.DeficitRequests, "steady", s.Recovery.Steady)
	return s, nil
}

// checkRouter refuses a run whose router would make its result meaningless: one
// that is not running the policy the run will be recorded under, does not front
// the replica the run would take away, or is already routing around a replica.
func (cfg ChaosConfig) checkRouter(ctx context.Context) (router.Stats, error) {
	stats, err := fetchRouterStats(ctx, cfg.Router)
	if err != nil {
		return router.Stats{}, fmt.Errorf("bench: the router at %s did not answer, so there is nothing to run a chaos test against: %w", cfg.Router, err)
	}
	if stats.Policy != cfg.Policy {
		return router.Stats{}, fmt.Errorf("bench: the router at %s is running %q, but this chaos run would be recorded as %q, "+
			"and a directory named for one policy holding another's recovery would make the comparison a policy against itself",
			cfg.Router, stats.Policy, cfg.Policy)
	}
	var ids []string
	fronted := false
	for _, r := range stats.Replicas {
		ids = append(ids, r.ID)
		fronted = fronted || r.ID == cfg.Replica
		if !r.InRotation() {
			return router.Stats{}, fmt.Errorf("bench: the router at %s is already routing around %s (%s), so the run would start from a fleet that is not whole and its baseline would not be one", cfg.Router, r.ID, r.Rotation())
		}
	}
	if !fronted {
		return router.Stats{}, fmt.Errorf("bench: the router at %s does not front %s (it fronts %s), so the fault would be injected into nothing and the fleet's steady state reported as a recovery",
			cfg.Router, cfg.Replica, strings.Join(ids, ", "))
	}
	// The replica comes back from its restart with an empty cache, and a prefix
	// index goes on believing what it held until those beliefs expire. Brought
	// back inside that time, it would have sessions sent to it on the strength of
	// blocks the restart emptied — a second dip belonging to the TTL rather than
	// to the policy, in exactly the comparison the run exists to make.
	if index := stats.PrefixIndex; index != nil && cfg.RecoverAt-cfg.FaultAt < index.TTL {
		return router.Stats{}, fmt.Errorf("bench: the replica would be restarted %v after the fault, inside the prefix index's %v TTL, so the index could still credit it with blocks its restart emptied and the run would measure the TTL rather than the policy. Restart it at least %v after the fault",
			cfg.RecoverAt-cfg.FaultAt, index.TTL, index.TTL)
	}
	return stats, nil
}

// injectAndRecover takes the replica away at FaultAt and brings it back at
// RecoverAt, recording each step as it happens.
func (cfg ChaosConfig) injectAndRecover(ctx context.Context, start time.Time, events *eventLog) error {
	if !waitUntil(ctx, start.Add(cfg.FaultAt)) {
		return ctx.Err()
	}
	switch cfg.Fault {
	case FaultKill:
		events.add(EventFault, "killed")
	case FaultDrain:
		events.add(EventFault, "drain requested")
		if err := rotate(ctx, cfg.Router, cfg.Replica, "drain"); err != nil {
			return err
		}
		cfg.awaitDrained(ctx, events)
	}
	if err := cfg.Stop(ctx); err != nil {
		return fmt.Errorf("stop %s: %w", cfg.Replica, err)
	}
	events.add(EventStopped, "")

	if !waitUntil(ctx, start.Add(cfg.RecoverAt)) {
		return ctx.Err()
	}
	events.add(EventRecovering, "")
	if err := cfg.Start(ctx); err != nil {
		return fmt.Errorf("start %s: %w", cfg.Replica, err)
	}
	events.add(EventStarted, "")
	// A drained replica stays out until it is restored, whatever its health
	// checks say, so the run restores it. An ejected one the router readmits by
	// itself, which is #19's criterion 6 and is the thing a kill run watches for.
	if cfg.Fault == FaultDrain {
		return rotate(ctx, cfg.Router, cfg.Replica, "restore")
	}
	return nil
}

// awaitDrained waits for the drained replica's in-flight count to reach zero.
//
// If it does not within DrainTimeout the replica is stopped anyway, and the
// requests it still held are dropped and counted: a drain that cannot finish is
// a finding, and waiting on it for ever would lose the run.
func (cfg ChaosConfig) awaitDrained(ctx context.Context, events *eventLog) {
	deadline := time.Now().Add(cfg.DrainTimeout)
	held := -1
	for time.Now().Before(deadline) {
		if stats, err := fetchRouterStats(ctx, cfg.Router); err == nil {
			for _, r := range stats.Replicas {
				if r.ID != cfg.Replica {
					continue
				}
				if held = r.Inflight; held == 0 {
					events.add(EventDrained, "")
					return
				}
			}
		}
		if !waitUntil(ctx, time.Now().Add(chaosPollInterval)) {
			return
		}
	}
	// Recorded, not only logged: the report's claim about the drain is worded
	// from whether it finished, and a drain that did not must not be reported as
	// one that kept its promise.
	detail := fmt.Sprintf("%d still in flight after %v", held, cfg.DrainTimeout)
	if held < 0 {
		detail = fmt.Sprintf("its in-flight count could not be read within %v", cfg.DrainTimeout)
	}
	events.add(EventDrainTimedOut, detail)
	cfg.Log.Warn("the drain did not finish in time, so the replica is being stopped with requests still on it", "replica", cfg.Replica, "detail", detail)
}

// watchRotation records every time the router takes the replica out of rotation
// or puts it back, until the context is done.
func (cfg ChaosConfig) watchRotation(ctx context.Context, events *eventLog) {
	ticker := time.NewTicker(chaosPollInterval)
	defer ticker.Stop()
	// checkRouter saw every replica in rotation before the run began.
	var out router.ReplicaStats
	in := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stats, err := fetchRouterStats(ctx, cfg.Router)
		if err != nil {
			continue
		}
		for _, r := range stats.Replicas {
			if r.ID != cfg.Replica {
				continue
			}
			switch now := r.InRotation(); {
			case in && !now:
				events.add(EventOutOfRotation, r.Rotation())
			case !in && now:
				// Named for the last reason it was out: a replica the operator
				// drained is restored, one the health checks ejected is readmitted.
				back := "readmitted"
				if out.Draining {
					back = "restored"
				}
				events.add(EventInRotation, back)
			}
			in = r.InRotation()
			if !in {
				out = r
			}
		}
	}
}

// eventLog collects a run's events as they happen, from the goroutine
// injecting the fault and the one watching the router.
type eventLog struct {
	mu     sync.Mutex
	events []timedEvent
}

type timedEvent struct {
	at     time.Time
	kind   EventKind
	detail string
}

func (l *eventLog) add(kind EventKind, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, timedEvent{at: time.Now(), kind: kind, detail: detail})
}

// relativeTo is the log timed from the fault.
func (l *eventLog) relativeTo(fault time.Time) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, len(l.events))
	for _, e := range l.events {
		out = append(out, Event{AtNs: e.at.Sub(fault).Nanoseconds(), Kind: e.kind, Detail: e.detail})
	}
	return out
}

// fetchRouterStats reads a router's published stats.
func fetchRouterStats(ctx context.Context, base string) (router.Stats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/router/stats", nil)
	if err != nil {
		return router.Stats{}, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return router.Stats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return router.Stats{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var stats router.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return router.Stats{}, fmt.Errorf("not a kvroute router's stats: %w", err)
	}
	return stats, nil
}

// rotate asks the router to drain or restore a replica.
func rotate(ctx context.Context, base, replica, action string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/router/replicas/"+replica+"/"+action, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", action, replica, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: the router answered %d", action, replica, resp.StatusCode)
	}
	return nil
}
