package bench_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/residency"
	"github.com/yuchia329/kvroute/internal/router"
)

// ADR-0010: a fleet publishing its KV cache events is a different engine
// configuration from one that is not — the engine builds and sends the events on
// every scheduler step — so exact residency, which cannot run without them, is
// compared only with cells recorded under the same setting. These are the places
// that promise is kept: the cell says which it was, a sweep will not resume cells
// recorded under the other, and compare will not put the two in one table.

// cachedClosedLoopCell is a finished round-robin cell at one concurrency, recorded
// with or without the fleet publishing its events.
func cachedClosedLoopCell(kvEvents bool) bench.Cell {
	return bench.Cell{
		ID:          "round_robin-c1-r1",
		Policy:      "round_robin",
		Driver:      bench.ClosedLoopDriver,
		Concurrency: 1,
		Repetition:  1,
		Workload:    bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}).Name(),
		KVEvents:    kvEvents,
	}
}

// sweepWithFleetEvents asks a sweep to resume into a directory, and returns what it
// refused to do. Like sweepAtThinkTime it never reaches a router: the cached-cell
// checks run first.
func sweepWithFleetEvents(t *testing.T, dir string, on bool) error {
	t.Helper()
	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           dir,
		Target:        "http://127.0.0.1:1", // never reached
		Policy:        "round_robin",
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  20 * time.Millisecond,
		FleetKVEvents: on,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return err
}

func TestACellRecordsWhetherTheFleetPublishedKVEvents(t *testing.T) {
	cells, _ := sweepUnderTest(t, t.TempDir(), bench.SweepConfig{FleetKVEvents: true})
	if len(cells) == 0 {
		t.Fatal("the sweep recorded no cells")
	}
	for _, c := range cells {
		if !c.KVEvents {
			t.Errorf("cell %s ran on a fleet publishing KV cache events and does not say so", c.ID)
		}
	}
}

// A cell recorded without the events is the events-off reference. A sweep of an
// events-on fleet that resumed it would report it cached, send nothing, and pool
// an events-off measurement into an events-on comparison — the exact trap of
// re-running policy 4 for #24 into #18's directory.
func TestASweepRefusesToResumeCellsRecordedUnderTheOtherEngineSetting(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedClosedLoopCell(false))

	err := sweepWithFleetEvents(t, dir, true)
	if err == nil || !strings.Contains(err.Error(), "KV cache events") {
		t.Errorf("err = %v, want a refusal naming the KV cache events setting", err)
	}
}

func TestASweepUnderTheSameEngineSettingResumes(t *testing.T) {
	dir := t.TempDir()
	writeCachedCell(t, dir, cachedClosedLoopCell(true))

	// Past the cached-cell checks, so whatever it fails on next is the router it
	// was never given — not the engine setting.
	if err := sweepWithFleetEvents(t, dir, true); err != nil && strings.Contains(err.Error(), "KV cache events") {
		t.Fatalf("cells recorded under the sweep's own setting were refused: %v", err)
	}
}

func TestCellsRecordedWithAndWithoutKVEventsAreNotOneComparison(t *testing.T) {
	load := bench.ClosedLoopAt(32)
	approx := cells(policy.PrefixAffinityName, load, 10, 10, 10)
	exact := cells(policy.ExactResidencyName, load, 12, 12, 12)
	for i := range exact {
		exact[i].KVEvents = true
	}

	_, err := bench.Compare(append(approx, exact...))
	if err == nil || !strings.Contains(err.Error(), "KV cache events") {
		t.Errorf("err = %v, want a refusal naming the KV cache events setting", err)
	}
}

// A router that is following the engines' events is proof the fleet publishes
// them, so a sweep that would label its cells as run without them is told so
// before the first cell rather than after the last.
func TestASweepRefusesToLabelCellsEventsOffAgainstARouterFollowingEvents(t *testing.T) {
	stats := router.Stats{
		Policy:    policy.ExactResidencyName,
		Spill:     &policy.Spill{LoadImbalanceFactor: 2},
		Replicas:  []router.ReplicaStats{{ID: "replica-0"}},
		Residency: []residency.FeedStats{{ReplicaStats: residency.ReplicaStats{Replica: "replica-0"}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/router/stats" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(stats)
	}))
	t.Cleanup(srv.Close)

	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           t.TempDir(),
		Target:        srv.URL,
		Policy:        policy.ExactResidencyName,
		Spill:         policy.Spill{LoadImbalanceFactor: 2},
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  20 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil || !strings.Contains(err.Error(), "KV cache events") {
		t.Errorf("err = %v, want a refusal naming the KV cache events setting", err)
	}
}
