package bench_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fakereplica"
)

// countingReplica is a fake replica whose prefix-cache counters advance with the
// prompts it is sent, which the plain fake's do not: it models no cache and
// reports both counters at zero on purpose. A cell's hit rate is a difference
// between two readings, so the fixture has to move.
type countingReplica struct {
	inner   http.Handler
	served  atomic.Int64
	metrics atomic.Bool
}

func (c *countingReplica) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/metrics" {
		if !c.metrics.Load() {
			http.Error(w, "no metrics here", http.StatusNotFound)
			return
		}
		// Three queries per request, one of them answered from the cache: a
		// third is a rate no arithmetic here could produce by accident.
		served := c.served.Load()
		fmt.Fprintf(w, "vllm:prefix_cache_hits_total{model_name=\"m\"} %d\n", served)
		fmt.Fprintf(w, "vllm:prefix_cache_queries_total{model_name=\"m\"} %d\n", served*3)
		return
	}
	if req.URL.Path == "/v1/chat/completions" {
		c.served.Add(1)
	}
	c.inner.ServeHTTP(w, req)
}

// countedFleet runs one replica that counts, behind a router, and returns the
// router's URL and the replica's own.
func countedFleet(t *testing.T, metrics bool) (target, replica string, counter *countingReplica) {
	t.Helper()
	counter = &countingReplica{inner: fakereplica.New(fakereplica.Config{}).Handler()}
	counter.metrics.Store(metrics)
	srv := httptest.NewServer(counter)
	t.Cleanup(srv.Close)
	return routerFor(t, "replica-0="+srv.URL), srv.URL, counter
}

func prefixSweepAt(t *testing.T, target, replica string) bench.Cell {
	t.Helper()
	cells, err := bench.RunSweep(t.Context(), bench.SweepConfig{
		Dir:           t.TempDir(),
		Target:        target,
		Policy:        "round_robin",
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  80 * time.Millisecond,
		Replicas:      []string{replica},
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, OutputTokens: 2}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("run sweep: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("ran %d cells, want 1", len(cells))
	}
	return cells[0]
}

// The prefix cache hit rate is vLLM's own reported figure and the ground truth
// the policy comparison is read against, so a cell has to carry the fleet's
// counters over its own window rather than a lifetime total.
func TestACellRecordsWhatTheFleetsPrefixCacheServedOverIt(t *testing.T) {
	target, replica, counter := countedFleet(t, true)
	cell := prefixSweepAt(t, target, replica)

	got := cell.PrefixCache()
	if !got.Read {
		t.Fatal("the replica's counters were scrapeable but the cell records no prefix-cache evidence")
	}
	if got.Queries == 0 {
		t.Fatal("the cell recorded a window in which the replica queried its prefix cache no times, though it served requests")
	}
	if served := counter.served.Load(); served == 0 {
		t.Fatal("the fixture served no requests")
	}
	// The fixture answers one query in three from cache, so anything else means
	// the cell is reporting a window other than its own.
	if rate := got.HitRate(); rate < 0.32 || rate > 0.34 {
		t.Errorf("cell hit rate is %.3f over %v hits and %v queries, want ~0.333", rate, got.Hits, got.Queries)
	}
}

// A fleet whose counters cannot be read leaves the cell saying so. Recording it
// as a hit rate of zero would put a scrape failure in the column the policy
// comparison is read from, and nothing downstream could tell the two apart.
func TestACellWhoseCountersCouldNotBeReadSaysSoRatherThanReportingZero(t *testing.T) {
	target, replica, _ := countedFleet(t, false)
	cell := prefixSweepAt(t, target, replica)

	if got := cell.PrefixCache(); got.Read {
		t.Errorf("the replica served no /metrics, but the cell records the reading %v", got)
	}
	if got := cell.PrefixCache(); got.Evidenced() {
		t.Error("a cell with no readable counters claims prefix-cache evidence")
	}
}

// A sweep given no replica URLs cannot scrape anything, and says that rather
// than reporting a fleet-wide hit rate of zero for every cell it runs.
func TestASweepWithNoReplicaURLsRecordsNoPrefixCacheEvidence(t *testing.T) {
	cells, _ := sweepUnderTest(t, t.TempDir(), bench.SweepConfig{
		Concurrencies: []int{1},
		CellDuration:  40 * time.Millisecond,
	})
	for _, cell := range cells {
		if cell.PrefixCache().Read {
			t.Errorf("cell %s claims a prefix-cache reading though the sweep was given no replicas to scrape", cell.ID)
		}
	}
}
