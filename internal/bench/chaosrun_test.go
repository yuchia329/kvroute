package bench_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/yuchia329/kvroute/internal/fakereplica/fakereplicatest"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// chaosFleet runs three fake replicas behind a router running the given policy
// with its health checks running, and returns the router and the one replica
// that can be killed. Everything above the replicas' HTTP surface is the real thing: the
// router, its fleet, its health checks and the harness driving it.
func chaosFleet(t *testing.T, chosen policy.Policy) (string, *fakereplicatest.Mortal) {
	t.Helper()
	cfg := func(id string) fakereplica.Config {
		// Streams long enough that a kill always finds some requests before their
		// first token and some in the middle of one.
		return fakereplica.Config{ID: id, TTFT: 20 * time.Millisecond, InterToken: 40 * time.Millisecond, OutputTokens: 8}
	}
	var specs []string
	for _, id := range []string{"replica-0", "replica-1"} {
		srv := httptest.NewServer(fakereplica.New(cfg(id)).Handler())
		t.Cleanup(srv.Close)
		specs = append(specs, id+"="+srv.URL)
	}
	doomed := fakereplicatest.Start(t, fakereplica.New(cfg("replica-2")).Handler())
	specs = append(specs, "replica-2="+doomed.URL())

	replicas, err := fleet.ParseSpecs(specs)
	if err != nil {
		t.Fatalf("parse specs: %v", err)
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	rt, err := router.New(router.Config{Fleet: f, Policy: chosen, Records: record.NewWriter[record.Request](nopWriter{}), Logger: quiet})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go fleet.CheckHealth(ctx, fleet.HealthConfig{Fleet: f, Interval: 20 * time.Millisecond, Timeout: time.Second, Log: quiet})
	return srv.URL, doomed
}

// chaosConfig is a short run: faulted at 1.5 s, restarted at 2 s, over at 3 s,
// in quarter-second buckets.
func chaosConfig(dir, target string, fault bench.Fault, doomed *fakereplicatest.Mortal) bench.ChaosConfig {
	return bench.ChaosConfig{
		Dir:         dir,
		Router:      target,
		Policy:      policy.RoundRobinName,
		Replica:     "replica-2",
		Fault:       fault,
		ArrivalRate: 40,
		ThinkTime:   100 * time.Millisecond,
		Duration:    3 * time.Second,
		Warmup:      500 * time.Millisecond,
		FaultAt:     1500 * time.Millisecond,
		RecoverAt:   2 * time.Second,
		Bucket:      250 * time.Millisecond,
		Tolerance:   0.10,
		SLO:         bench.SLO{TTFT: 2 * time.Second, ITL: time.Second},
		Workload:    bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 8}),
		Stop:        func(context.Context) error { doomed.Kill(); return nil },
		Start:       func(context.Context) error { return doomed.Revive() },
		Log:         quiet,
	}
}

// The whole of #19 in one run: a replica is killed under load, the router finds
// out and routes around it, the replica is restarted and the router takes it back
// by itself, and the run comes out as a curve and a report rather than a pair of
// numbers.
func TestAChaosRunKillsAReplicaAndRecordsItsRecovery(t *testing.T) {
	target, doomed := chaosFleet(t, policy.NewRoundRobin())
	dir := t.TempDir()

	s, err := bench.RunChaos(context.Background(), chaosConfig(dir, target, bench.FaultKill, doomed))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Noticed gone, and noticed back, with nothing restarted but the replica.
	if out, ok := s.After(bench.EventOutOfRotation); !ok || out > time.Second {
		t.Errorf("out of rotation after %v (seen=%v): the router did not take the killed replica out promptly", out, ok)
	}
	started, restarted := s.After(bench.EventStarted)
	back, readmitted := s.After(bench.EventInRotation)
	if !restarted || !readmitted {
		t.Fatalf("restarted=%v readmitted=%v: the replica was not brought back into rotation by itself", restarted, readmitted)
	}
	if back < started {
		t.Errorf("back in rotation at %v, before it was restarted at %v", back, started)
	}

	if s.Policy != policy.RoundRobinName {
		t.Errorf("policy = %q, want the one the router reports", s.Policy)
	}
	// The kill reached the traffic, and all of what it cost is accounted for.
	// Which way a request went depends on where it was at the instant of the kill
	// — before its first token it is rerouted, mid-stream it is dropped, and the
	// first broken stream ejects the replica so no later request reaches it — so
	// the split is the run's to report, and the router's own tests pin each path
	// down. What must hold every time is that the fault cost something and none
	// of it is booked as a replica answering with an error.
	if s.Summary.Rerouted+s.Summary.Dropped == 0 {
		t.Error("the kill cost nothing — no request was rerouted or dropped — so it never reached the traffic")
	}
	if s.Summary.Failed != 0 {
		t.Errorf("%d requests failed: a replica dying is not a replica answering with an error", s.Summary.Failed)
	}
	if len(s.Curve) == 0 || s.Curve[0].AtNs >= 0 || s.Curve[len(s.Curve)-1].AtNs <= 0 {
		t.Errorf("the curve does not span the fault: %+v", s.Curve)
	}

	report, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(report), s.DropAccounting()) {
		t.Error("the written report does not carry the drop accounting")
	}
	var recorded bench.ChaosRun
	contents, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatalf("read the run record: %v", err)
	}
	if err := json.Unmarshal(contents, &recorded); err != nil || recorded.Summary.Requests != s.Summary.Requests {
		t.Errorf("run.json does not hold the run (err=%v, requests %d vs %d)", err, recorded.Summary.Requests, s.Summary.Requests)
	}
	if _, err := os.Stat(filepath.Join(dir, "rows.jsonl")); err != nil {
		t.Errorf("the run's rows were not kept: %v", err)
	}
}

// Criterion 2 at the scale of a run: a replica drained before it is stopped
// finishes everything it held, so taking it away costs no request at all — even
// when the stop that follows is a kill.
func TestAChaosRunDrainsAReplicaWithoutDroppingAnything(t *testing.T) {
	target, doomed := chaosFleet(t, policy.NewRoundRobin())

	s, err := bench.RunChaos(context.Background(), chaosConfig(t.TempDir(), target, bench.FaultDrain, doomed))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if s.Summary.Dropped != 0 {
		t.Errorf("%d requests were dropped by a drain: %s", s.Summary.Dropped, s.DropAccounting())
	}
	drained, ok := s.After(bench.EventDrained)
	stopped, _ := s.After(bench.EventStopped)
	if !ok || stopped < drained {
		t.Errorf("drained at %v (seen=%v), stopped at %v: the replica must finish its work before it is stopped", drained, ok, stopped)
	}
	if _, ok := s.After(bench.EventInRotation); !ok {
		t.Error("the drained replica was never restored to rotation")
	}
}

// A run naming a replica the router does not front would inject its fault into
// nothing and report the fleet's steady state as a recovery. Refused before any
// load is offered.
func TestAChaosRunRefusesAReplicaTheRouterDoesNotFront(t *testing.T) {
	target, doomed := chaosFleet(t, policy.NewRoundRobin())
	cfg := chaosConfig(t.TempDir(), target, bench.FaultKill, doomed)
	cfg.Replica = "replica-9"

	if _, err := bench.RunChaos(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "replica-9") {
		t.Errorf("err = %v, want a refusal naming replica-9", err)
	}
}

// The run is told which policy the router runs rather than setting it, as a sweep
// is, and a directory named for one policy holding another's recovery would make
// the comparison a policy against itself. Refused before any load is offered.
func TestAChaosRunRefusesARouterRunningADifferentPolicy(t *testing.T) {
	target, doomed := chaosFleet(t, policy.NewRoundRobin())
	cfg := chaosConfig(t.TempDir(), target, bench.FaultKill, doomed)
	cfg.Policy = policy.SessionAffinityName

	_, err := bench.RunChaos(context.Background(), cfg)
	if err == nil {
		t.Fatal("a run labelled session_affinity ran against a round-robin router")
	}
	for _, want := range []string{policy.RoundRobinName, policy.SessionAffinityName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

// Each chaos run sends from a slice of the workload's user space its fault and
// rate earn it, so two policies' runs of one scenario send identical bytes and no
// other run shares them (ADR-0004). A rate that is not a whole number, or one past
// the slices a fault has, would land on another run's slice, so it is refused
// before anything runs.
func TestAChaosRunRefusesARateThatWouldShareAnotherRunsPrompts(t *testing.T) {
	for _, rate := range []float64{8.5, 600} {
		cfg := chaosConfig(t.TempDir(), "http://127.0.0.1:1", bench.FaultKill, nil)
		cfg.ArrivalRate = rate
		if _, err := bench.RunChaos(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "rate") {
			t.Errorf("rate %g: err = %v, want a refusal about the rate", rate, err)
		}
	}
}

// A replica taken away and restarted comes back with an empty cache, while the
// prefix index goes on believing what it held until those beliefs expire. A run
// that brought the replica back inside that time would have prefix affinity send
// sessions to a cold replica on the strength of a belief the restart made false,
// and would be measuring the index's TTL rather than the policy. So a run against
// a router with an index refuses to restart the replica inside its TTL.
func TestAChaosRunWillNotRestartAReplicaBeforeTheIndexHasForgottenIt(t *testing.T) {
	index, err := prefix.New(prefix.Config{NodeCap: 1024, TTL: 10 * time.Second})
	if err != nil {
		t.Fatalf("new index: %v", err)
	}
	target, doomed := chaosFleet(t, policy.NewPrefixAffinity(index, policy.Spill{}))
	cfg := chaosConfig(t.TempDir(), target, bench.FaultKill, doomed)
	cfg.Policy = policy.PrefixAffinityName

	if _, err := bench.RunChaos(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "TTL") {
		t.Errorf("err = %v, want a refusal naming the index's TTL", err)
	}
}

// A run that does not finish leaves its rows under a name that says so. They are
// still the record of what happened, but under the finished name — or in a file a
// second run went on to append to — an unfinished run could be read as a result.
func TestAChaosRunThatDoesNotFinishLeavesNoFinishedRows(t *testing.T) {
	target, doomed := chaosFleet(t, policy.NewRoundRobin())
	dir := t.TempDir()
	cfg := chaosConfig(dir, target, bench.FaultKill, doomed)
	cfg.Start = func(context.Context) error { return errors.New("the replica would not start") }

	if _, err := bench.RunChaos(context.Background(), cfg); err == nil {
		t.Fatal("a run whose replica never came back reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, bench.ChaosRows)); err == nil {
		t.Error("an unfinished run's rows are under the finished name")
	}
	if _, err := os.Stat(filepath.Join(dir, bench.ChaosRows+".partial")); err != nil {
		t.Errorf("an unfinished run's rows were not kept under their partial name: %v", err)
	}
}
