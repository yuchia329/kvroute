package bench_test

import (
	"context"
	"log/slog"
	"net/http"
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
	for i, r := range results {
		if r.ScheduledAtNs == 0 {
			t.Fatalf("row %d carries no due time, so nothing in the record shows the schedule was held", i)
		}
		// The k-th request is due at start + k/rate, computed from the cell's
		// own origin rather than accumulated, so lateness cannot compound.
		if lag := r.ScheduleLag(); lag > 60*time.Millisecond {
			t.Errorf("request %d was sent %v after it was due: the driver fell behind its own schedule", i, lag)
		}
		if lag := r.ScheduleLag(); lag < 0 {
			t.Errorf("request %d was sent %v before it was due", i, lag)
		}
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
