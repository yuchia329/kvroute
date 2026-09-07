package bench_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// inflight wraps a handler and reports the most requests it ever held at once.
type inflight struct {
	http.Handler
	now   atomic.Int64
	peak  atomic.Int64
	total atomic.Int64
}

func (i *inflight) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	i.total.Add(1)
	n := i.now.Add(1)
	for {
		peak := i.peak.Load()
		if n <= peak || i.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer i.now.Add(-1)
	i.Handler.ServeHTTP(w, r)
}

// fleetUnderTest runs one fake replica behind a round-robin router and returns
// the router's URL plus the replica's inflight counter. The replica HTTP
// boundary is the project's only test seam: the driver, the router and the
// policy below it all run unmodified.
func fleetUnderTest(t *testing.T, cfg fakereplica.Config) (string, *fakereplica.Replica, *inflight) {
	t.Helper()
	replica := fakereplica.New(cfg)
	counted := &inflight{Handler: replica.Handler()}
	replicaSrv := httptest.NewServer(counted)
	t.Cleanup(replicaSrv.Close)

	return routerFor(t, "replica-0="+replicaSrv.URL), replica, counted
}

// routerFor runs a round-robin router over the given replica specs.
func routerFor(t *testing.T, specs ...string) string {
	t.Helper()
	replicas, err := fleet.ParseSpecs(specs)
	if err != nil {
		t.Fatalf("parse replica specs: %v", err)
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	rt, err := router.New(router.Config{
		Fleet:   f,
		Policy:  policy.NewRoundRobin(),
		Records: record.NewWriter[record.Request](nopWriter{}),
		// Quiet: the drop test is meant to produce drops, and the router logs
		// every one of them at error level.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// drive runs a closed-loop cell against target and returns its rows.
func drive(t *testing.T, cfg bench.DriverConfig) []bench.Result {
	t.Helper()
	if cfg.Workload == nil {
		cfg.Workload = bench.NewFixedWorkload(bench.FixedWorkload{OutputTokens: 4})
	}
	results, err := bench.RunClosedLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run closed loop: %v", err)
	}
	return results
}

func TestTheDriverHoldsExactlyTheConfiguredNumberOfRequestsInFlight(t *testing.T) {
	target, _, counted := fleetUnderTest(t, fakereplica.Config{TTFT: 20 * time.Millisecond, InterToken: 2 * time.Millisecond})

	results := drive(t, bench.DriverConfig{
		Target:      target,
		Concurrency: 4,
		Duration:    300 * time.Millisecond,
	})

	// A closed-loop driver holds N virtual users, each sending its next request
	// only after the previous response completes. Never more than N, and — with
	// a duration long enough for several turns each — never fewer at the peak.
	if peak := counted.peak.Load(); peak != 4 {
		t.Errorf("the replica saw a peak of %d concurrent requests, want exactly 4", peak)
	}
	if len(results) < 4 {
		t.Fatalf("got %d results, want at least one per virtual user", len(results))
	}

	// Each virtual user is strictly sequential: its requests never overlap.
	byUser := map[int][]bench.Result{}
	for _, r := range results {
		byUser[r.VirtualUser] = append(byUser[r.VirtualUser], r)
	}
	if len(byUser) != 4 {
		t.Errorf("results came from %d virtual users, want 4", len(byUser))
	}
	for user, rows := range byUser {
		for i := 1; i < len(rows); i++ {
			if prev, next := rows[i-1], rows[i]; next.StartedAtNs < prev.StartedAtNs+prev.TotalNs {
				t.Errorf("virtual user %d started turn %d before turn %d finished", user, i, i-1)
			}
		}
	}
}

func TestARequestTheReplicaAcceptedAndThenErroredIsCountedAsFailed(t *testing.T) {
	target, replica, _ := fleetUnderTest(t, fakereplica.Config{})
	replica.SetFailure(&fakereplica.Failure{Status: http.StatusInternalServerError, Type: "InternalServerError", Message: "engine died"})

	results := drive(t, bench.DriverConfig{Target: target, Concurrency: 1, Duration: 50 * time.Millisecond})

	if len(results) == 0 {
		t.Fatal("no results")
	}
	for _, r := range results {
		if r.Outcome != record.OutcomeFailed {
			t.Fatalf("outcome is %q, want failed: a replica that accepted and then errored is not a drop", r.Outcome)
		}
		if r.Replica == "" {
			t.Error("the row does not say which replica failed")
		}
	}
}

func TestARequestTheRouterCouldNotPlaceIsCountedAsDropped(t *testing.T) {
	// A replica the router cannot reach. The router answers 502 without ever
	// having placed the request, which is a drop and not a replica failure.
	target := routerFor(t, "replica-0=http://127.0.0.1:1")

	results := drive(t, bench.DriverConfig{Target: target, Concurrency: 1, Duration: 50 * time.Millisecond})

	if len(results) == 0 {
		t.Fatal("no results")
	}
	for _, r := range results {
		if r.Outcome != record.OutcomeDropped {
			t.Fatalf("outcome is %q, want dropped", r.Outcome)
		}
	}
}

func TestTheRowRecordsWhichReplicaTheRouterChoseAndWhy(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})

	results := drive(t, bench.DriverConfig{Target: target, Concurrency: 1, Duration: 50 * time.Millisecond})

	if len(results) == 0 {
		t.Fatal("no results")
	}
	got := results[0]
	if got.Replica != "replica-0" {
		t.Errorf("row records replica %q, want replica-0", got.Replica)
	}
	if got.Decision != string(policy.ReasonRoundRobin) {
		t.Errorf("row records decision %q, want %q", got.Decision, policy.ReasonRoundRobin)
	}
	if got.RequestID == "" {
		t.Error("row carries no request id, so it cannot be joined to the router's own row")
	}
}

func TestTimingsAreMeasuredFromTheClientSide(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{
		TTFT:         120 * time.Millisecond,
		InterToken:   30 * time.Millisecond,
		OutputTokens: 6,
	})

	results := drive(t, bench.DriverConfig{
		Target:      target,
		Concurrency: 1,
		Duration:    10 * time.Millisecond,
		Workload:    bench.NewFixedWorkload(bench.FixedWorkload{OutputTokens: 6}),
	})

	if len(results) == 0 {
		t.Fatal("no results")
	}
	got := results[0]
	if got.Outcome != record.OutcomeSuccess {
		t.Fatalf("outcome is %q (%s), want success", got.Outcome, got.Error)
	}
	if ttft := time.Duration(got.TTFTNs); ttft < 120*time.Millisecond {
		t.Errorf("client-observed TTFT is %v, want at least the replica's 120ms", ttft)
	}
	if got.OutputTokens != 6 {
		t.Errorf("counted %d output tokens, want 6", got.OutputTokens)
	}
	if itl := time.Duration(got.ITLP50Ns); itl < 30*time.Millisecond || itl > 200*time.Millisecond {
		t.Errorf("inter-token latency p50 is %v, want it near the replica's 30ms", itl)
	}
	if got.ResponseBytes == 0 {
		t.Error("the row records no response bytes")
	}
}

func TestTheDriverLetsInFlightRequestsFinishInsteadOfCancellingThem(t *testing.T) {
	// The duration expires long before the response does. Cutting the request
	// there would put the driver's own clock in the failure column, so a
	// virtual user stops starting new turns at the deadline rather than
	// abandoning the one it is on.
	target, _, _ := fleetUnderTest(t, fakereplica.Config{TTFT: 150 * time.Millisecond})

	results := drive(t, bench.DriverConfig{Target: target, Concurrency: 2, Duration: time.Millisecond})

	if len(results) != 2 {
		t.Fatalf("got %d results, want one per virtual user", len(results))
	}
	for _, r := range results {
		if r.Outcome != record.OutcomeSuccess {
			t.Errorf("outcome is %q (%s), want success", r.Outcome, r.Error)
		}
	}
}

func TestWarmupRequestsAreMarkedButStillRecorded(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})

	results := drive(t, bench.DriverConfig{
		Target:      target,
		Concurrency: 1,
		Duration:    300 * time.Millisecond,
		Warmup:      2,
	})

	if len(results) < 3 {
		t.Fatalf("got %d results, want more than the warm-up count", len(results))
	}
	warm := 0
	for _, r := range results {
		if r.Warmup {
			warm++
		}
	}
	if warm != 2 {
		t.Errorf("marked %d rows as warm-up, want 2", warm)
	}
	if results[0].Warmup != true || results[2].Warmup != false {
		t.Error("warm-up marks are not the first requests each virtual user sent")
	}
}

func TestEveryRowIsWrittenToTheRecordAsWellAsReturned(t *testing.T) {
	target, _, _ := fleetUnderTest(t, fakereplica.Config{})
	rows := &countingSink{}

	results := drive(t, bench.DriverConfig{
		Target:      target,
		Concurrency: 2,
		Duration:    100 * time.Millisecond,
		Rows:        record.NewWriter[bench.Result](rows),
	})

	if got := rows.lines(); got != len(results) {
		t.Errorf("wrote %d rows for %d results", got, len(results))
	}
}

type countingSink struct {
	mu sync.Mutex
	n  int
}

func (s *countingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			s.n++
		}
	}
	return len(p), nil
}

func (s *countingSink) lines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
