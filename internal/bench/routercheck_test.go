package bench_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/policy"
)

// sweepAgainst runs the shortest possible sweep against a target, and reports what
// it refused to do.
func sweepAgainst(t *testing.T, target, policyName string) ([]bench.Cell, string, error) {
	t.Helper()
	dir := t.TempDir()
	cells, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           dir,
		Target:        target,
		Policy:        policyName,
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  20 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return cells, dir, err
}

// TestASweepRefusesARouterRunningADifferentPolicy is the guard on the sweep's
// weakest link.
//
// A router is started with its policy and the sweep is only *told* which one that
// was, so nothing but this check stands between a mistyped -policy and a directory
// of cells naming a policy that never ran. Two policies' cells would then differ in
// nothing at all, and the comparison table drawn from them would be fiction that no
// later analysis could detect — the numbers are all real, they just came from one
// policy twice.
func TestASweepRefusesARouterRunningADifferentPolicy(t *testing.T) {
	_, replicaURL := fakeReplicaServer(t)
	roundRobin := routerFor(t, "replica-0="+replicaURL)

	cells, dir, err := sweepAgainst(t, roundRobin, policy.LeastOutstandingName)

	if err == nil {
		t.Fatal("a sweep labelled least_outstanding ran against a round-robin router without complaint")
	}
	for _, want := range []string{policy.RoundRobinName, policy.LeastOutstandingName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q so the operator can see which is wrong", err, want)
		}
	}
	if len(cells) != 0 {
		t.Errorf("%d cells were run before the mismatch was caught", len(cells))
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "cells")); len(entries) != 0 {
		t.Errorf("%d cell files were written for a sweep that should not have started", len(entries))
	}
}

// TestASweepRefusesARouterThatIsNotThere. A dead router answers every request with
// a refused connection, which the driver books as a dropped request — so the sweep
// would run to completion, take as long as it was told to, and produce nothing but
// drops. On a shared box that is a wasted night, which is the same argument the
// fleet check already makes.
func TestASweepRefusesARouterThatIsNotThere(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	cells, _, err := sweepAgainst(t, deadURL, policy.RoundRobinName)

	if err == nil {
		t.Fatal("a sweep against a router that is not running was allowed to start")
	}
	if !strings.Contains(err.Error(), deadURL) {
		t.Errorf("err = %v, want it to name the router it could not reach", err)
	}
	if len(cells) != 0 {
		t.Errorf("%d cells were run against a dead router", len(cells))
	}
}

// TestASweepAgainstTheRouterItSaysItIsDrivingRuns is the happy path: the check
// passes when the label is the truth.
func TestASweepAgainstTheRouterItSaysItIsDrivingRuns(t *testing.T) {
	_, replicaURL := fakeReplicaServer(t)
	roundRobin := routerFor(t, "replica-0="+replicaURL)

	cells, _, err := sweepAgainst(t, roundRobin, policy.RoundRobinName)

	if err != nil {
		t.Fatalf("a correctly labelled sweep was refused: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("%d cells, want 1", len(cells))
	}
	if cells[0].Policy != policy.RoundRobinName {
		t.Errorf("cell policy = %q, want %q", cells[0].Policy, policy.RoundRobinName)
	}
}

// fakeReplicaServer runs one fake replica and returns it with its URL.
func fakeReplicaServer(t *testing.T) (*fakereplica.Replica, string) {
	t.Helper()
	replica := fakereplica.New(fakereplica.Config{ID: "replica-0", OutputTokens: 1})
	srv := httptest.NewServer(replica.Handler())
	t.Cleanup(srv.Close)
	return replica, srv.URL
}
