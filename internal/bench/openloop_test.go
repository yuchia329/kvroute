package bench_test

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/record"
)

// slowing wraps a replica and makes it slower with every request it serves, so
// a cell can contain a fleet that degrades under the load being offered to it
// rather than one that was slow from the start.
//
// This is the condition the two drivers are told apart by. A closed-loop driver
// waits for a response before sending the next request, so as this replica
// slows the offered load falls with it and the fleet is never pushed past its
// knee — coordinated omission, and the reason the closed-loop tail is
// systematically optimistic.
type slowing struct {
	http.Handler
	step time.Duration
	n    atomic.Int64
}

func (s *slowing) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case <-time.After(time.Duration(s.n.Add(1)) * s.step):
	case <-r.Context().Done():
		return
	}
	s.Handler.ServeHTTP(w, r)
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// openLoop runs one open-loop cell against target.
func openLoop(t *testing.T, cfg bench.DriverConfig) []bench.Result {
	t.Helper()
	if cfg.Workload == nil {
		cfg.Workload = bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 1})
	}
	cfg.Log = discardLog()
	results, err := bench.RunOpenLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run open loop: %v", err)
	}
	return results
}

// The property the open-loop driver exists for: the arrival schedule is an
// input, so it survives a fleet that slows underneath it.
func TestTheOpenLoopDriverHoldsItsScheduleWhileTheFleetSlowsUnderIt(t *testing.T) {
	const (
		rate     = 100.0
		duration = 400 * time.Millisecond
	)
	target, counted := slowingFleet(t, 20*time.Millisecond)

	results := openLoop(t, bench.DriverConfig{Target: target, ArrivalRate: rate, Duration: duration})

	// The fleet really did fall behind the schedule: requests piled up on it far
	// past the one at a time a closed-loop driver at concurrency 1 would have
	// held. Without this the assertions below could pass against a fleet that
	// was keeping up.
	if peak := counted.peak.Load(); peak < 10 {
		t.Fatalf("the replica never held more than %d requests at once, so it was not slower than the arrival gap and this test proves nothing", peak)
	}

	// Every arrival the schedule called for was fired, however slow the fleet
	// became: at 100/s over 400ms that is forty of them.
	want := int(rate * duration.Seconds())
	if len(results) != want {
		t.Errorf("fired %d requests against a schedule of %d: the driver did not hold its arrival rate", len(results), want)
	}
	var lags []time.Duration
	for i, r := range results {
		if r.ScheduledAtNs == 0 {
			t.Fatalf("row %d carries no due time, so nothing in the record shows the schedule was held", i)
		}
		lag := r.ScheduleLag()
		if lag < 0 {
			t.Errorf("request %d was sent %v before it was due", i, lag)
		}
		// The k-th request is due at start + k/rate, computed from the cell's
		// own origin rather than accumulated, so lateness cannot compound. This
		// bound is loose because it has to survive a loaded machine under the
		// race detector; the median below is what says the schedule was held
		// rather than merely not abandoned.
		if lag > 60*time.Millisecond {
			t.Errorf("request %d was sent %v after it was due: the driver fell behind its own schedule", i, lag)
		}
		lags = append(lags, lag)
	}

	// A driver that waits on the fleet drifts monotonically behind, so its
	// median lag grows into the hundreds of milliseconds this fleet takes to
	// answer. Holding the median to a fraction of one 10ms arrival gap is the
	// assertion that the schedule was kept rather than approximated.
	slices.Sort(lags)
	if median := lags[len(lags)/2]; median > 5*time.Millisecond {
		t.Errorf("the median request was sent %v after it was due, against a 10ms arrival gap: the driver is pacing off the fleet rather than off its schedule", median)
	}
}

// The same slowed fleet, driven both ways. The closed-loop driver throttles
// itself against it and the open-loop one does not, which is the whole reason
// both exist.
func TestAClosedLoopDriverThrottlesItselfOnTheFleetTheOpenLoopDriverKeepsLoading(t *testing.T) {
	const duration = 400 * time.Millisecond

	slowClosed, _ := slowingFleet(t, 20*time.Millisecond)
	slowOpen, _ := slowingFleet(t, 20*time.Millisecond)

	closed := drive(t, bench.DriverConfig{
		Target:      slowClosed,
		Concurrency: 4,
		Duration:    duration,
		Workload:    bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 1}),
	})
	open := openLoop(t, bench.DriverConfig{
		Target:      slowOpen,
		ArrivalRate: 100,
		Duration:    duration,
	})

	if len(open) <= 2*len(closed) {
		t.Errorf("the open-loop driver offered %d requests and the closed-loop one %d over the same window against the same slowed fleet: the open-loop driver is throttling itself too",
			len(open), len(closed))
	}
	// The closed-loop rows carry no due time because nothing scheduled them:
	// under that driver the next request is due when the last one finished.
	for _, r := range closed {
		if r.ScheduledAtNs != 0 {
			t.Errorf("a closed-loop row claims it was due at %d", r.ScheduledAtNs)
		}
	}
}

// One schema, so both drivers can populate one table. The row says which driver
// produced it and at what load, because a goodput figure whose driver is
// unknown is not interpretable.
func TestRowsFromBothDriversShareOneSchemaAndSayWhichDriverProducedThem(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{TTFT: time.Millisecond, OutputTokens: 1})
	labels := bench.Labels{CellID: "round_robin-a20-r1", Policy: "round_robin", Repetition: 1}

	open := openLoop(t, bench.DriverConfig{Target: target, ArrivalRate: 20, Duration: 100 * time.Millisecond, Labels: labels})
	closed := drive(t, bench.DriverConfig{Target: target, Concurrency: 2, Duration: 100 * time.Millisecond, Labels: labels})

	if len(open) == 0 || len(closed) == 0 {
		t.Fatalf("got %d open-loop and %d closed-loop rows", len(open), len(closed))
	}
	for _, r := range open {
		if r.Driver != bench.OpenLoopDriver {
			t.Errorf("open-loop row names driver %q", r.Driver)
		}
		if r.ArrivalRate != 20 {
			t.Errorf("open-loop row records an arrival rate of %g, want 20", r.ArrivalRate)
		}
		if r.Concurrency != 0 {
			t.Errorf("open-loop row claims a concurrency of %d, which is an outcome under this driver and not an input", r.Concurrency)
		}
	}
	for _, r := range closed {
		if r.Driver != bench.ClosedLoopDriver {
			t.Errorf("closed-loop row names driver %q", r.Driver)
		}
		if r.ArrivalRate != 0 {
			t.Errorf("closed-loop row claims an offered rate of %g, which is an outcome under this driver and not an input", r.ArrivalRate)
		}
	}

	// The same arithmetic reads both, which is what "one table" means.
	both := append(append([]bench.Result{}, open...), closed...)
	if got := bench.Summarize(both, bench.SummaryOptions{}).Requests; got != len(both) {
		t.Errorf("one summary over both drivers' rows counted %d of %d requests", got, len(both))
	}
}

// The deadline stops new arrivals; it never cuts one that is already in flight.
// Cutting there would put the driver's own clock in the failure column.
func TestTheOpenLoopDriverLetsInFlightRequestsFinishInsteadOfCancellingThem(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{TTFT: 150 * time.Millisecond, OutputTokens: 1})

	results := openLoop(t, bench.DriverConfig{Target: target, ArrivalRate: 10, Duration: time.Millisecond})

	if len(results) != 1 {
		t.Fatalf("fired %d requests in a window one arrival long, want 1", len(results))
	}
	if got := results[0].Outcome; got != record.OutcomeSuccess {
		t.Errorf("outcome is %q (%s), want success", got, results[0].Error)
	}
}

func TestTheOpenLoopDriverRefusesToRunWithoutAnArrivalRate(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})

	_, err := bench.RunOpenLoop(context.Background(), bench.DriverConfig{
		Target:   target,
		Duration: 10 * time.Millisecond,
		Workload: bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel}),
		Log:      discardLog(),
	})

	if err == nil {
		t.Fatal("the open-loop driver ran with no arrival rate, which is its only input")
	}
	if !strings.Contains(err.Error(), "arrival rate") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// slowingFleet is a router over one replica that gets slower with every request
// it serves, and the counter that says how many it was holding at once.
func slowingFleet(t *testing.T, step time.Duration) (string, *inflight) {
	t.Helper()
	replica := fakereplica.New(fakereplica.Config{OutputTokens: 1})
	counted := &inflight{Handler: &slowing{Handler: replica.Handler(), step: step}}
	return routerOver(t, counted), counted
}

// Arrivals have to advance conversations, not restart them. A driver that
// offered every session's first turn and no session's second would hand the
// policies a workload with no growing prefix to be aware of — the multi-turn
// generator's whole point, silently discarded by the load model.
func TestOpenLoopArrivalsAdvanceConversationsInsteadOfRepeatingFirstTurns(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{OutputTokens: 1})
	sessions, err := bench.NewMultiTurn(bench.MultiTurnWorkload{
		Model: testModel, Sessions: 32, TurnsPerSession: 4, Skew: 1, OutputTokens: 1,
	})
	if err != nil {
		t.Fatalf("build the multi-turn workload: %v", err)
	}

	// 100/s for 600ms is 60 arrivals; a 100ms think time holds ten
	// conversations open, so each should walk several turns.
	results := openLoop(t, bench.DriverConfig{
		Target: target, ArrivalRate: 100, Duration: 600 * time.Millisecond,
		ThinkTime: 100 * time.Millisecond, Workload: sessions,
	})

	turns := map[int]int{}
	perSession := map[string]map[int]bool{}
	for _, r := range results {
		turns[r.Turn]++
		if perSession[r.Session] == nil {
			perSession[r.Session] = map[int]bool{}
		}
		perSession[r.Session][r.Turn] = true
	}
	if len(results) == 0 {
		t.Fatal("no rows")
	}
	if turns[0] == len(results) {
		t.Fatalf("every one of %d arrivals was a first turn: the load model discards the multi-turn workload's sessions", len(results))
	}
	// Some session was carried through successive turns, which is what gives a
	// prefix-aware policy something to preserve.
	deepest := 0
	for _, seen := range perSession {
		deepest = max(deepest, len(seen))
	}
	if deepest < 2 {
		t.Errorf("no session was offered more than one of its turns: turn histogram %v", turns)
	}
}

// The pool is a think time rather than a count, because the gap between a
// session's turns means the same thing at every rate and a count does not.
func TestTheConversationPoolFollowsTheRateAndTheThinkTime(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{OutputTokens: 1})

	// 50/s with a 200ms think time is ten conversations, so ten distinct
	// sessions carry all forty arrivals.
	results := openLoop(t, bench.DriverConfig{
		Target: target, ArrivalRate: 50, Duration: 800 * time.Millisecond,
		ThinkTime: 200 * time.Millisecond,
	})

	sessions := map[string]bool{}
	for _, r := range results {
		sessions[r.Session] = true
	}
	if len(sessions) != 10 {
		t.Errorf("%d arrivals were spread over %d conversations, want the 10 that 50/s at a 200ms think time makes", len(results), len(sessions))
	}
}

// ADR-0004: a cell's slice of the workload's user space is finite, and a pool
// past the end of it would send the next cell's prompts.
func TestAConversationPoolPastTheCellsSliceOfTheWorkloadIsRefused(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})

	_, err := bench.RunOpenLoop(context.Background(), bench.DriverConfig{
		Target: target, ArrivalRate: 500, Duration: time.Second,
		ThinkTime: time.Minute,
		Workload:  bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel}),
		Log:       discardLog(),
	})

	if err == nil {
		t.Fatal("a conversation pool larger than the cell's slice of the workload was accepted")
	}
	if !strings.Contains(err.Error(), "pool") {
		t.Errorf("the error does not say what was too large: %v", err)
	}
}
