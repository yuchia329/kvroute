package bench_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/kvevents"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/residency"
	"github.com/yuchia329/kvroute/internal/router"
)

// ADR-0010's cold start, measured cell by cell. Exact residency forgets a
// replica's residency whenever its event stream loses history or reconnects, and
// rebuilds it from what follows — safe, because it can only forget, but a cell
// run across one of those moments ran on an index that knew less than it claimed,
// and nothing in its goodput would say so. So the router's residency figures are
// read either side of every cell, as its ejection count is, and a cell they moved
// in is flagged rather than averaged in.

// residencyOf is one replica's residency as the router reports it.
func residencyOf(stream kvevents.Stats, orphaned uint64) residency.FeedStats {
	return residency.FeedStats{
		ReplicaStats: residency.ReplicaStats{Replica: "replica-0", Blocks: 100, Orphaned: orphaned},
		Stream:       stream,
	}
}

var healthyStream = kvevents.Stats{Connected: true, Connections: 1, Applied: 10}

// routerReportingResidency fronts a real router, answering /router/stats with that
// router's own stats plus a residency section, and passing everything else
// through. The section reads before until the first chat completion has passed,
// and after from then on, so a cell sees the stream change inside its own window
// and nowhere else.
func routerReportingResidency(t *testing.T, before, after residency.FeedStats) string {
	t.Helper()
	_, replicaURL := fakeReplicaServer(t)
	real := routerFor(t, "replica-0="+replicaURL)
	target, err := url.Parse(real)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	var served atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/router/stats" {
			if r.URL.Path == router.ChatCompletionsPath {
				served.Store(true)
			}
			proxy.ServeHTTP(w, r)
			return
		}
		resp, err := http.Get(real + "/router/stats")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		var stats router.Stats
		if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		reported := before
		if served.Load() {
			reported = after
		}
		stats.Residency = []residency.FeedStats{reported}
		_ = json.NewEncoder(w).Encode(stats)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// sweepOneCell runs a one-cell sweep against a target on a fleet publishing its
// events, and returns the cell.
func sweepOneCell(t *testing.T, target string) bench.Cell {
	t.Helper()
	cells, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           t.TempDir(),
		Target:        target,
		Policy:        policy.RoundRobinName,
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  150 * time.Millisecond,
		FleetKVEvents: true,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 2}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("%d cells, want 1", len(cells))
	}
	return cells[0]
}

func TestACellDuringWhichExactResidencyLostHistoryIsFlagged(t *testing.T) {
	lossy := healthyStream
	lossy.Lost, lossy.Resets = 5, 1
	cell := sweepOneCell(t, routerReportingResidency(t, residencyOf(healthyStream, 0), residencyOf(lossy, 0)))

	if !cell.ResidencyRead {
		t.Error("the cell says its residency was never read, so its counts would mean nothing")
	}
	if cell.ResidencyLost != 5 || cell.ResidencyResets != 1 {
		t.Errorf("the cell records %d batches lost and %d resets, want 5 and 1", cell.ResidencyLost, cell.ResidencyResets)
	}
	if !cell.Flagged || !mentions(cell.FlagReasons, "lost") {
		t.Errorf("a cell during which exact residency lost history is not flagged for it: %q", cell.FlagReasons)
	}
}

// A stream that reconnects starts its replica from nothing, which is the same
// incompleteness by another route.
func TestACellDuringWhichAnEventStreamReconnectedIsFlagged(t *testing.T) {
	reconnected := healthyStream
	reconnected.Connections = 2
	cell := sweepOneCell(t, routerReportingResidency(t, residencyOf(healthyStream, 0), residencyOf(reconnected, 0)))

	if cell.ResidencyReconnects != 1 {
		t.Errorf("the cell records %d reconnects, want 1", cell.ResidencyReconnects)
	}
	if !mentions(cell.FlagReasons, "reconnected") {
		t.Errorf("a cell during which a stream reconnected is not flagged for it: %q", cell.FlagReasons)
	}
}

// A stream that was not connected when the cell ended left the index following
// nothing for that replica.
func TestACellWhoseEventStreamEndedDisconnectedIsFlagged(t *testing.T) {
	dropped := healthyStream
	dropped.Connected = false
	cell := sweepOneCell(t, routerReportingResidency(t, residencyOf(healthyStream, 0), residencyOf(dropped, 0)))

	if !slices.Equal(cell.ResidencyDisconnected, []string{"replica-0"}) {
		t.Errorf("the cell records %q disconnected at its end, want replica-0", cell.ResidencyDisconnected)
	}
	if !mentions(cell.FlagReasons, "not connected") {
		t.Errorf("a cell that ended with a stream disconnected is not flagged for it: %q", cell.FlagReasons)
	}
}

// Orphaned runs are recorded and do not flag. A duplicate block evicted while its
// twin lives on orphans the next run stored on top of it, and that is the index
// forgetting a block it could not vouch for — the direction ADR-0010 chose —
// rather than history lost.
func TestACellWhoseResidencyStayedCompleteIsNotFlaggedForIt(t *testing.T) {
	grew := healthyStream
	grew.Applied = 40
	cell := sweepOneCell(t, routerReportingResidency(t, residencyOf(healthyStream, 0), residencyOf(grew, 3)))

	if !cell.ResidencyRead || cell.ResidencyOrphaned != 3 {
		t.Errorf("the cell records %d orphaned runs (read: %v), want 3", cell.ResidencyOrphaned, cell.ResidencyRead)
	}
	if mentions(cell.FlagReasons, "exact residency") {
		t.Errorf("a cell whose residency stayed complete was flagged for it: %q", cell.FlagReasons)
	}
}
