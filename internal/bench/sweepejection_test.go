package bench_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fakereplica/fakereplicatest"
	"github.com/yuchia329/kvroute/internal/policy"
)

// ADR-0009: health checks run in every router, the frozen comparison's
// included, so a replica can now leave the fleet in the middle of a cell. The
// router reroutes around the gap rather than dropping into it, which is right for
// clients and dangerous for the record: nothing in the cell's figures would show
// that it measured a smaller fleet for part of its window. So the router's
// ejection count is read either side of every cell, and a cell during which it
// moved is flagged rather than averaged in.
func TestACellDuringWhichAReplicaWasEjectedIsFlagged(t *testing.T) {
	doomed := fakereplicatest.Start(t, fakereplica.New(fakereplica.Config{ID: "replica-1", OutputTokens: 2}).Handler())
	_, alive := fakeReplicaServer(t)
	target := routerFor(t, "replica-0="+alive, "replica-1="+doomed.URL())
	// A tenth of a second into a cell of four.
	go func() {
		time.Sleep(100 * time.Millisecond)
		doomed.Kill()
	}()

	cells, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           t.TempDir(),
		Target:        target,
		Policy:        policy.RoundRobinName,
		Concurrencies: []int{2},
		Repetitions:   1,
		CellDuration:  400 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 2}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	cell := cells[0]
	if cell.Ejections != 1 {
		t.Errorf("the cell records %d ejections, want 1", cell.Ejections)
	}
	if !cell.EjectionsRead {
		t.Error("the cell says its ejection count was never read, so its count would mean nothing")
	}
	if !cell.Flagged || !mentions(cell.FlagReasons, "ejected") {
		t.Errorf("a cell during which a replica was ejected is not flagged for it: flagged=%v reasons=%q", cell.Flagged, cell.FlagReasons)
	}
}

// And the cheaper half of the same guard: a sweep does not start against a router
// that is already routing around a missing replica, because every one of its
// cells would measure a smaller fleet than it says.
func TestASweepRefusesARouterWithAReplicaOutOfRotation(t *testing.T) {
	_, first := fakeReplicaServer(t)
	_, second := fakeReplicaServer(t)
	target := routerFor(t, "replica-0="+first, "replica-1="+second)
	resp, err := http.Post(target+"/router/replicas/replica-1/drain", "", nil)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	resp.Body.Close()

	cells, _, err := sweepAgainst(t, target, policy.RoundRobinName)

	if err == nil || !strings.Contains(err.Error(), "replica-1") {
		t.Errorf("err = %v, want a refusal naming replica-1", err)
	}
	if len(cells) != 0 {
		t.Errorf("%d cells ran against a fleet with a replica out of rotation", len(cells))
	}
}

// A replica that dies in one cell and is not brought back leaves every later cell
// on a smaller fleet, and after the first none of them sees an ejection of its
// own to be flagged for. So the cell records the replicas it ended without, and
// the sweep stops before the next cell rather than measure a fleet its cells do
// not describe. The cells already recorded are kept, and a later pass resumes
// once the replica is back.
func TestASweepStopsRatherThanMeasureAFleetMissingAReplica(t *testing.T) {
	doomed := fakereplicatest.Start(t, fakereplica.New(fakereplica.Config{ID: "replica-1", OutputTokens: 2}).Handler())
	_, alive := fakeReplicaServer(t)
	target := routerFor(t, "replica-0="+alive, "replica-1="+doomed.URL())
	go func() {
		time.Sleep(100 * time.Millisecond)
		doomed.Kill()
	}()

	cells, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           t.TempDir(),
		Target:        target,
		Policy:        policy.RoundRobinName,
		Concurrencies: []int{2},
		Repetitions:   2,
		CellDuration:  400 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 2}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	if err == nil || !strings.Contains(err.Error(), "replica-1") {
		t.Errorf("err = %v, want the sweep stopped, naming replica-1", err)
	}
	if len(cells) != 1 {
		t.Fatalf("%d cells were recorded, want only the one the replica died in", len(cells))
	}
	if got := cells[0].OutOfRotation; len(got) != 1 || got[0] != "replica-1" {
		t.Errorf("the cell records %q out of rotation at its end, want replica-1", got)
	}
}

func mentions(reasons []string, word string) bool {
	for _, r := range reasons {
		if strings.Contains(r, word) {
			return true
		}
	}
	return false
}
