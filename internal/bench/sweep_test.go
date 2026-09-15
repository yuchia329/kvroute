package bench_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

// sweepUnderTest runs a sweep against a fake fleet in a temporary directory,
// returning the cells, the directory and the replica's request counter.
func sweepUnderTest(t *testing.T, dir string, cfg bench.SweepConfig) ([]bench.Cell, *inflight) {
	t.Helper()
	target, _, counted := fleetUnderTest(t, fakereplica.Config{})

	cfg.Dir = dir
	cfg.Target = target
	if cfg.Policy == "" {
		cfg.Policy = "round_robin"
	}
	if cfg.Concurrencies == nil {
		cfg.Concurrencies = []int{1, 2}
	}
	if cfg.Repetitions == 0 {
		cfg.Repetitions = 1
	}
	if cfg.CellDuration == 0 {
		cfg.CellDuration = 20 * time.Millisecond
	}
	if cfg.Workload == nil {
		cfg.Workload = bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2})
	}
	cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	cells, err := bench.RunSweep(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run sweep: %v", err)
	}
	return cells, counted
}

func TestASweepResumesFromCachedCellsWithoutRecomputingThem(t *testing.T) {
	dir := t.TempDir()

	first, firstReplica := sweepUnderTest(t, dir, bench.SweepConfig{})
	if len(first) != 2 {
		t.Fatalf("first pass produced %d cells, want 2", len(first))
	}
	if firstReplica.total.Load() == 0 {
		t.Fatal("the first pass sent no requests")
	}

	// A second pass over the same directory: a sweep that was interrupted after
	// eleven hours must not start the twelfth from the beginning.
	second, secondReplica := sweepUnderTest(t, dir, bench.SweepConfig{})

	if got := secondReplica.total.Load(); got != 0 {
		t.Errorf("the second pass sent %d requests, want none: cached cells were recomputed", got)
	}
	if len(second) != len(first) {
		t.Fatalf("second pass produced %d cells, want %d", len(second), len(first))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Requests != second[i].Requests {
			t.Errorf("cell %s came back changed: %+v then %+v", first[i].ID, first[i].Summary, second[i].Summary)
		}
	}
}

func TestASweepRecomputesACellThatNeverFinished(t *testing.T) {
	dir := t.TempDir()

	first, _ := sweepUnderTest(t, dir, bench.SweepConfig{})

	// Simulate a crash partway through one cell: its rows are on disk but the
	// cell record that marks it complete was never written.
	victim := first[1].ID
	if err := os.Remove(filepath.Join(dir, "cells", victim+".json")); err != nil {
		t.Fatalf("remove cell record: %v", err)
	}

	_, replica := sweepUnderTest(t, dir, bench.SweepConfig{})

	if replica.total.Load() == 0 {
		t.Error("the interrupted cell was treated as cached and never re-run")
	}
}

func TestTheRowsOfEveryCellAreOnDiskAsJSONL(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	path := filepath.Join(dir, "cells", cells[0].ID+".jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cell rows: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != cells[0].Requests+cells[0].Warmup {
		t.Errorf("%s holds %d rows, want %d", path, len(lines), cells[0].Requests+cells[0].Warmup)
	}
	var row bench.Result
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("row is not valid JSON: %v", err)
	}
	if row.CellID != cells[0].ID {
		t.Errorf("row is stamped with cell %q, want %q", row.CellID, cells[0].ID)
	}
}

func TestACellRecordsTheForeignProcessesItSawAndIsNotClean(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{1},
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Contaminated(),
			Interval: time.Millisecond,
			GPUs:     []int{0},
		},
	})

	got := cells[0]
	if got.Clean {
		t.Error("a cell that ran beside another user's process reported itself clean")
	}
	if len(got.ForeignProcs) == 0 {
		t.Error("the cell records no foreign process")
	}
	if got.MaxForeignGPUMemMiB != 8999 {
		t.Errorf("peak foreign memory is %d MiB, want 8999", got.MaxForeignGPUMemMiB)
	}
	if !got.Flagged {
		t.Error("an unclean cell was not flagged")
	}
}

// A card that is slower than its siblings because it is hot is a defect of a
// different kind from another user's process, and a cell can carry both or
// either. This one is perfectly clean and still must not be averaged in.
func TestACleanCellIsStillFlaggedWhenOneOfItsCardsWasThrottled(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies:        []int{1},
		WarmupDriftThreshold: -1,
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Throttled(),
			Interval: time.Millisecond,
			GPUs:     []int{0, 1, 2, 3, 4, 5},
			OwnPIDs:  gputest.OwnFleetPIDs,
		},
	})

	got := cells[0]
	if !got.Clean {
		t.Errorf("a cell with no foreign process was reported unclean: %v", got.ForeignProcs)
	}
	if !got.Throttled {
		t.Errorf("a cell whose GPU 3 ran at 960 MHz on a thermal limit was not marked throttled: %+v", got.Throttle)
	}
	if !got.Flagged {
		t.Error("a throttled cell was not flagged")
	}
	// The evidence, not just the verdict: which card, how much of the time, and
	// how far down it clocked.
	over := got.Throttle.Thermal()
	if len(over) != 1 || over[0].GPU != 3 || over[0].MinSMClockMHz != 960 {
		t.Errorf("the cell names %+v as throttled, want GPU 3 at 960 MHz", over)
	}
}

// The other half of the same distinction: every card in a loaded fleet sits on
// its power cap, and a cell that ran on six healthy cards must not be discarded
// for it.
func TestALoadedFleetOnItsPowerCapIsNotFlagged(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies:        []int{1},
		WarmupDriftThreshold: -1,
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Busy(),
			Interval: time.Millisecond,
			GPUs:     []int{0, 1, 2, 3, 4, 5},
			OwnPIDs:  gputest.OwnFleetPIDs,
		},
	})

	got := cells[0]
	if got.Throttled {
		t.Errorf("a healthy loaded fleet was marked throttled: %+v", got.Throttle)
	}
	if got.Flagged {
		t.Errorf("a healthy loaded fleet was flagged: %v", got.FlagReasons)
	}
	if got.ClockSamples == 0 || len(got.Throttle.GPUs) != 6 {
		t.Errorf("the cell recorded %d clock samples over %d cards, want every card carried in the record",
			got.ClockSamples, len(got.Throttle.GPUs))
	}
}

func TestACellIsNotClaimedCleanWhenTheGPUsWereNeverSampled(t *testing.T) {
	dir := t.TempDir()

	// No prober: running against fakes on a machine with no GPU. Reporting
	// clean here would be the same word for "nothing was found" and "nothing
	// was looked for".
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	got := cells[0]
	if got.Clean {
		t.Error("a cell whose GPUs were never sampled reported itself clean")
	}
	if got.GPUSamples != 0 {
		t.Errorf("recorded %d GPU samples with no prober configured", got.GPUSamples)
	}
	if !got.Flagged {
		t.Error("a cell with no contamination evidence was not flagged")
	}
}

func TestACleanCellSaysSo(t *testing.T) {
	dir := t.TempDir()

	// Our own fleet, holding both cards, and nothing else.
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{1},
		// This cell is about the contamination evidence, and the warm-up drift
		// check is about a fleet's latency settling. Against a fake replica of
		// fixed latency, the only thing it can measure is how busy the machine
		// running the tests was during the first half of a sub-second cell, so
		// leaving it on makes this test fail on a loaded laptop.
		WarmupDriftThreshold: -1,
		Contamination: bench.ContaminationConfig{
			Prober:   gputest.Fleet(),
			Interval: time.Millisecond,
			GPUs:     []int{0, 1},
			OwnPIDs:  gputest.OwnFleetPIDs,
		},
	})

	got := cells[0]
	if !got.Clean {
		t.Errorf("our own fleet was reported as contamination: %v", got.ForeignProcs)
	}
	if got.GPUSamples == 0 {
		t.Error("the cell recorded no GPU samples")
	}
	if got.Flagged {
		t.Errorf("a clean cell was flagged: %v", got.FlagReasons)
	}
}

// sweepWith runs a sweep under a caller-supplied context, so a test can
// interrupt one mid-cell.
func sweepWith(t *testing.T, ctx context.Context, dir string, cfg bench.SweepConfig) ([]bench.Cell, error, *inflight) {
	t.Helper()
	target, _, counted := fleetUnderTest(t, fakereplica.Config{})
	cfg.Dir = dir
	cfg.Target = target
	cfg.Policy = "round_robin"
	cfg.Repetitions = 1
	cfg.Workload = bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2})
	cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	cells, err := bench.RunSweep(ctx, cfg)
	return cells, err, counted
}

func TestAnInterruptedCellIsNotCachedAsThoughItHadFinished(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	// Interrupt while the first cell is still running. Persisting it would bake
	// a truncated cell into the results permanently: the next pass would find a
	// cache entry and never re-run it.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	cells, err := func() ([]bench.Cell, error) {
		got, err, _ := sweepWith(t, ctx, dir, bench.SweepConfig{
			Concurrencies: []int{1},
			CellDuration:  2 * time.Second,
		})
		return got, err
	}()

	if err == nil {
		t.Error("an interrupted sweep reported success")
	}
	if len(cells) != 0 {
		t.Errorf("an interrupted sweep returned %d cells", len(cells))
	}
	id := bench.CellID("round_robin", bench.ClosedLoopAt(1), 1)
	if _, err := os.Stat(filepath.Join(dir, "cells", id+".json")); err == nil {
		t.Error("the interrupted cell was written as a completed cell record")
	}
	// Its rows survive, under a name that says what they are.
	if _, err := os.Stat(filepath.Join(dir, "cells", id+".jsonl.partial")); err != nil {
		t.Errorf("the interrupted cell's rows were not kept: %v", err)
	}

	// And the next pass runs it for real.
	_, replica := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})
	if replica.total.Load() == 0 {
		t.Error("the interrupted cell was treated as cached on the next pass")
	}
}

func TestAContaminatedCellIsDiscardedAndReRunRatherThanCached(t *testing.T) {
	dir := t.TempDir()

	contaminated := bench.SweepConfig{
		Concurrencies: []int{1},
		Contamination: bench.ContaminationConfig{
			Prober: gputest.Contaminated(), Interval: time.Millisecond, GPUs: []int{0},
		},
	}
	first, _ := sweepUnderTest(t, dir, contaminated)
	if !first[0].Contaminated() {
		t.Fatal("the first pass did not see the foreign process")
	}

	// idea.md §6: any cell with a foreign process on any of the six cards is
	// discarded and re-run. Leaving it cached would make "re-run" mean "delete
	// the file by hand first".
	clean := bench.SweepConfig{
		Concurrencies: []int{1},
		Contamination: bench.ContaminationConfig{
			Prober: gputest.Fleet(), Interval: time.Millisecond,
			GPUs: []int{0, 1}, OwnPIDs: gputest.OwnFleetPIDs,
		},
	}
	second, replica := sweepUnderTest(t, dir, clean)

	if replica.total.Load() == 0 {
		t.Error("the contaminated cell was loaded from cache instead of being re-run")
	}
	if !second[0].Clean {
		t.Errorf("the re-run cell is still unclean: %v", second[0].FlagReasons)
	}

	// The evidence for why it was thrown away survives, outside cells/ so that
	// compaction cannot pick it up as a result.
	discarded, err := filepath.Glob(filepath.Join(dir, "discarded", "*.json"))
	if err != nil || len(discarded) == 0 {
		t.Errorf("the discarded cell was deleted rather than kept: %v %v", discarded, err)
	}
}

func TestChangingTheSLORecomputesCachedCellsFromTheirRowsInsteadOfReRunningThem(t *testing.T) {
	dir := t.TempDir()

	// The concurrency-1 cell is what the SLO gets derived from, so it runs
	// before one exists.
	first, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})
	if first[0].SLOApplied {
		t.Fatal("the first pass applied an SLO it was not given")
	}

	// Applying the derived threshold must not cost another run: SLO violation
	// is a property of the rows, and the rows are the system of record.
	second, replica := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{1},
		SLO:           bench.SLO{TTFT: time.Nanosecond},
	})

	if got := replica.total.Load(); got != 0 {
		t.Errorf("applying an SLO re-ran the cell, sending %d requests", got)
	}
	if !second[0].SLOApplied {
		t.Error("the cached cell was not resummarised against the new SLO")
	}
	if second[0].SLOViolations != second[0].Successes {
		t.Errorf("counted %d violations against a 1ns TTFT threshold over %d successes, want all of them",
			second[0].SLOViolations, second[0].Successes)
	}
	// And the recomputation is persisted, so the next pass does not redo it.
	reloaded, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{1},
		SLO:           bench.SLO{TTFT: time.Nanosecond},
	})
	if !reloaded[0].SLOApplied || reloaded[0].SLOViolations != second[0].SLOViolations {
		t.Errorf("the resummarised cell was not written back: %+v", reloaded[0].Summary)
	}
}

// The sweep's load axis takes either driver, and a cell records which one ran
// it. Both axes in one directory is the case that has to work: the headline
// goodput number and the concurrency scaling are read side by side.
func TestASweepRunsBothLoadAxesAndEachCellSaysWhichDriverRanIt(t *testing.T) {
	dir := t.TempDir()

	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{
		Concurrencies: []int{2},
		ArrivalRates:  []float64{50},
		CellDuration:  60 * time.Millisecond,
	})

	if len(cells) != 2 {
		t.Fatalf("produced %d cells, want one per load level", len(cells))
	}
	closed, open := cells[0], cells[1]
	if closed.Driver != bench.ClosedLoopDriver || closed.Concurrency != 2 || closed.ArrivalRate != 0 {
		t.Errorf("the concurrency cell records driver %q, concurrency %d, rate %g", closed.Driver, closed.Concurrency, closed.ArrivalRate)
	}
	if open.Driver != bench.OpenLoopDriver || open.ArrivalRate != 50 || open.Concurrency != 0 {
		t.Errorf("the rate cell records driver %q, concurrency %d, rate %g", open.Driver, open.Concurrency, open.ArrivalRate)
	}
	// The ids say which axis a cell is on, so the two never collide in the
	// directory they resume from.
	if open.ID != bench.CellID("round_robin", bench.OpenLoopAt(50), 1) || open.ID == closed.ID {
		t.Errorf("cell ids %q and %q do not tell the two axes apart", closed.ID, open.ID)
	}
	if open.Requests+open.Warmup == 0 {
		t.Error("the open-loop cell sent nothing")
	}
	if open.Scheduled == 0 {
		t.Error("the open-loop cell records no scheduled requests, so nothing shows it held its rate")
	}
}

func TestASweepRefusesToStartWhenARateIsAboveWhatTheWorkloadIsPartitionedFor(t *testing.T) {
	dir := t.TempDir()
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})

	// Two cells whose slices of the workload's user space would overlap send
	// each other's bytes, and the second would be reading the replicas' prefix
	// caches rather than measuring prefill. Refusing beats discovering it in
	// the results.
	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir: dir, Target: target, Policy: "round_robin",
		ArrivalRates: []float64{4096},
		CellDuration: 20 * time.Millisecond,
		Workload:     bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	if err == nil {
		t.Fatal("the sweep accepted an arrival rate the workload cannot be partitioned for")
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Errorf("the error does not name the rate it refused: %v", err)
	}
}

func TestASweepRefusesToStartWhenTwoCellsWouldSendTheSamePrompts(t *testing.T) {
	dir := t.TempDir()
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})

	// Two rates a fraction apart are two cells by their ids and one slice of the
	// workload's user space by the arithmetic that partitions it. ADR-0004: the
	// second would read the replicas' prefix caches rather than measure prefill,
	// and its latency would not admit to it.
	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir: dir, Target: target, Policy: "round_robin",
		ArrivalRates: []float64{12.4, 12.44},
		CellDuration: 20 * time.Millisecond,
		Workload:     bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	if err == nil {
		t.Fatal("the sweep accepted two cells that would send each other's prompts")
	}
	if !strings.Contains(err.Error(), "same prompts") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "cells", "*.json")); len(entries) != 0 {
		t.Errorf("a cell ran before the partition was checked: %v", entries)
	}
}

func TestASweepRefusesToStartWhenAReplicaIsDown(t *testing.T) {
	dir := t.TempDir()
	// One replica up, one that nothing is listening on. Round-robin would send
	// every second request into the dead one, the router could not place it, and
	// the cell would fill with drops — after the hours it took to produce them.
	live := fakereplica.New(fakereplica.Config{})
	liveSrv := httptest.NewServer(live.Handler())
	t.Cleanup(liveSrv.Close)
	target := routerFor(t, "replica-0="+liveSrv.URL, "replica-1=http://127.0.0.1:1")

	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir: dir, Target: target, Policy: "round_robin",
		Concurrencies: []int{1}, CellDuration: 20 * time.Millisecond,
		Workload: bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Replicas: []string{liveSrv.URL, "http://127.0.0.1:1"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	if err == nil {
		t.Fatal("the sweep started with a replica down")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("the error does not name the unreachable replica: %v", err)
	}
	// Nothing was run, so nothing was cached.
	if entries, _ := filepath.Glob(filepath.Join(dir, "cells", "*.json")); len(entries) != 0 {
		t.Errorf("a cell was written despite the fleet being incomplete: %v", entries)
	}
}

func TestASweepRunsWhenEveryReplicaAnswers(t *testing.T) {
	dir := t.TempDir()
	live := fakereplica.New(fakereplica.Config{})
	liveSrv := httptest.NewServer(live.Handler())
	t.Cleanup(liveSrv.Close)

	cells, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir: dir, Target: routerFor(t, "replica-0="+liveSrv.URL), Policy: "round_robin",
		Concurrencies: []int{1}, CellDuration: 20 * time.Millisecond,
		Workload: bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Replicas: []string{liveSrv.URL},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("run sweep: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("produced %d cells, want 1", len(cells))
	}
}

func TestEveryReplicaIsWarmedBeforeTheFirstMeasuredRequest(t *testing.T) {
	dir := t.TempDir()

	// Six replicas, as on the box. Under round-robin at concurrency 1 a warm-up
	// routed through the router would touch replicas 0..4 and leave replica 5
	// to serve the first *measured* request cold — the warm-up manufacturing
	// the cold start it exists to prevent, in the cell that defines the floor.
	var specs []string
	var bases []string
	counters := make([]*inflight, 6)
	for i := range 6 {
		// Slow enough that the cell below sends exactly one request, so the
		// only way every replica gets touched is the fleet warm-up.
		replica := fakereplica.New(fakereplica.Config{TTFT: 20 * time.Millisecond})
		counters[i] = &inflight{Handler: replica.Handler()}
		srv := httptest.NewServer(counters[i])
		t.Cleanup(srv.Close)
		specs = append(specs, fmt.Sprintf("replica-%d=%s", i, srv.URL))
		bases = append(bases, srv.URL)
	}

	if _, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir: dir, Target: routerFor(t, specs...), Policy: "round_robin",
		Replicas:      bases,
		FleetWarmup:   2,
		Concurrencies: []int{1},
		CellDuration:  time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("run sweep: %v", err)
	}

	for i, counted := range counters {
		if got := counted.total.Load(); got < 2 {
			t.Errorf("replica %d served %d requests; it was never warmed", i, got)
		}
	}
}

func TestTheFleetWarmupFailsLoudlyWhenAReplicaCannotServe(t *testing.T) {
	dir := t.TempDir()
	live := fakereplica.New(fakereplica.Config{})
	live.SetFailure(&fakereplica.Failure{Status: 500, Type: "InternalServerError", Message: "engine died"})
	srv := httptest.NewServer(live.Handler())
	t.Cleanup(srv.Close)

	// /health still answers — vLLM's does even when generation is broken — so
	// the health check passes and only an actual request finds the problem.
	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir: dir, Target: routerFor(t, "replica-0="+srv.URL), Policy: "round_robin",
		Replicas:      []string{srv.URL},
		FleetWarmup:   1,
		Concurrencies: []int{1},
		CellDuration:  20 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	if err == nil {
		t.Fatal("the sweep ran against a replica that cannot generate")
	}
	if !strings.Contains(err.Error(), "warming the fleet") {
		t.Errorf("the error does not say the warm-up failed: %v", err)
	}
}
