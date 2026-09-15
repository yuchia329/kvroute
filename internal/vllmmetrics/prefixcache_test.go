package vllmmetrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

func counters(hits, queries string) string {
	return "# HELP vllm:prefix_cache_hits_total x\n" +
		"# TYPE vllm:prefix_cache_hits_total counter\n" +
		"vllm:prefix_cache_hits_total{model_name=\"m\"} " + hits + "\n" +
		"vllm:prefix_cache_queries_total{model_name=\"m\"} " + queries + "\n"
}

// serving returns a replica whose /metrics answers with the given bodies in
// turn, so a test can take a reading before a window and another after it.
func serving(t *testing.T, bodies ...string) string {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := bodies[min(n, len(bodies)-1)]
		n++
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/metrics"
}

func TestThePrefixCacheHitRateIsWhatTheReplicaServedOverTheWindow(t *testing.T) {
	url := serving(t, counters("100", "1000"), counters("400", "3000"))
	ctx := context.Background()

	before := vllmmetrics.ReadPrefixCache(ctx, nil, url)
	after := vllmmetrics.ReadPrefixCache(ctx, nil, url)

	got := after.Since(before)
	if !got.Read {
		t.Fatal("both readings succeeded but the delta reports nothing was read")
	}
	if got.Hits != 300 || got.Queries != 2000 {
		t.Errorf("delta is %v hits over %v queries, want 300 over 2000", got.Hits, got.Queries)
	}
	if want := 0.15; got.HitRate() != want {
		t.Errorf("prefix cache hit rate is %v, want %v: the window's own figure, not the replica's lifetime one", got.HitRate(), want)
	}
}

// "No hits" and "nobody looked" must not read the same. A cell whose counters
// could not be scraped has no prefix cache evidence, and reporting that as a
// prefix cache hit rate of zero would put a scrape failure in the column the policy
// comparison is read from.
func TestAnUnreadableReplicaReportsNoEvidenceRatherThanAZeroPrefixCacheHitRate(t *testing.T) {
	ctx := context.Background()

	got := vllmmetrics.ReadPrefixCache(ctx, nil, "http://127.0.0.1:1/metrics")
	if got.Read {
		t.Error("a replica that could not be reached reported a reading")
	}

	// A reading that never happened cannot be one end of a window either.
	real := vllmmetrics.ReadPrefixCache(ctx, nil, serving(t, counters("1", "2")))
	if real.Since(got).Read || got.Since(real).Read {
		t.Error("a delta was computed against a reading that was never taken")
	}
	if got.HitRate() != 0 {
		t.Error("an unread reading reported a non-zero prefix cache hit rate")
	}
}

// A counter that went backwards means the replica restarted inside the window.
// That is not a delta, and a cell that reported one would be reporting the
// fraction of a lifetime it never measured.
func TestARestartedReplicaYieldsNoDeltaRatherThanANegativeOne(t *testing.T) {
	ctx := context.Background()
	url := serving(t, counters("900", "5000"), counters("10", "50"))

	before := vllmmetrics.ReadPrefixCache(ctx, nil, url)
	after := vllmmetrics.ReadPrefixCache(ctx, nil, url)

	if got := after.Since(before); got.Read {
		t.Errorf("a replica whose counters went backwards produced the delta %v", got)
	}
}

// A window over which nothing queried the prefix cache is read but says nothing.
// Its prefix cache hit rate is arithmetically zero and means nothing, so a table that printed
// 0% for it would report a cache that was never exercised as one that never hit.
func TestAWindowWithNoQueriesIsReadButCarriesNoEvidence(t *testing.T) {
	empty := vllmmetrics.PrefixCache{Read: true}
	if !empty.Read {
		t.Fatal("the fixture is not a reading")
	}
	if empty.Evidenced() {
		t.Error("a window of zero queries claims to be evidence of a prefix cache hit rate")
	}
	if got := (vllmmetrics.PrefixCache{Hits: 1, Queries: 4, Read: true}); !got.Evidenced() {
		t.Error("a window of four queries is not treated as evidence")
	}
	if got := (vllmmetrics.PrefixCache{Hits: 1, Queries: 4}); got.Evidenced() {
		t.Error("an unread reading claims to be evidence")
	}
}

// A replica that answers but does not carry the counters is as unread as one
// that does not answer: the metric set has churned across vLLM releases, and a
// silently-zero prefix cache hit rate is exactly what this package exists to prevent.
func TestAReplicaMissingTheCountersIsUnread(t *testing.T) {
	url := serving(t, "vllm:num_requests_running{model_name=\"m\"} 3.0\n")

	if got := vllmmetrics.ReadPrefixCache(context.Background(), nil, url); got.Read {
		t.Errorf("a replica reporting neither counter was read as %v", got)
	}
}

// Adding up two replicas' evidence is how a fleet-wide figure is formed, and one
// replica nobody could read makes the whole pool unread: a prefix cache hit rate averaged
// over some replicas that were checked and some that were not is a number
// nobody can say what is behind.
func TestPoolingIsUnreadIfAnyPartOfItIs(t *testing.T) {
	read := vllmmetrics.PrefixCache{Hits: 10, Queries: 100, Read: true}
	other := vllmmetrics.PrefixCache{Hits: 30, Queries: 100, Read: true}

	pooled := vllmmetrics.PoolPrefixCache([]vllmmetrics.PrefixCache{read, other})
	if !pooled.Read || pooled.Hits != 40 || pooled.Queries != 200 {
		t.Errorf("pooled two readings into %v, want 40 hits over 200 queries", pooled)
	}
	if want := 0.2; pooled.HitRate() != want {
		t.Errorf("pooled prefix cache hit rate is %v, want %v", pooled.HitRate(), want)
	}

	if got := vllmmetrics.PoolPrefixCache([]vllmmetrics.PrefixCache{read, {}}); got.Read {
		t.Errorf("pooling a read replica with an unread one produced %v", got)
	}
	if got := vllmmetrics.PoolPrefixCache(nil); got.Read {
		t.Errorf("pooling nothing produced the reading %v", got)
	}
}
