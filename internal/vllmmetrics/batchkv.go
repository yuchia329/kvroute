package vllmmetrics

import (
	"context"
	"fmt"
	"net/http"
)

// BatchKVUsage is the gauge saying how much of a replica's KV cache is
// allocated to the requests it is currently running.
//
// It is a load signal, and the name says so because the old one did not.
// Through #16 and #18 this was read as memory pressure — how close a replica
// was to being out of cache room — and it is not that. The allocator counts
// blocks held by *running* requests; blocks holding the cached prefixes of
// finished requests are free from its point of view, reclaimable and waiting to
// be reused or evicted, and they are exactly the blocks a prefix match depends
// on. Measured over 13,658 routing decisions the gauge came to
// 0.021 + 0.0214 × inflight at r = 0.973: it is the active batch, which the
// router already counts exactly and locally, in different units. ADR-0011 has
// the full account and #28 has the evidence.
//
// It is kept rather than dropped because it is still the one *engine-side* view
// of load the router has, and reading it beside the router's own inflight is
// what any later claim about the two has to rest on. Nothing routes on it.
//
// It is NOT vllm:gpu_cache_usage_perc. The name churned across vLLM releases and
// the engine still publishes both; the other one is not this figure, and a
// scrape that silently matched it would report a number nobody could trace.
const BatchKVUsage = "vllm:kv_cache_usage_perc"

// BatchOccupancy is one scrape of one replica's batch KV occupancy: the
// fraction of its cache blocks allocated to the requests it is running.
//
// Read is false when the gauge could not be scraped, so "an idle replica" and
// "nobody could reach this replica" do not read the same. It is a recorded
// column rather than a routed-on signal, and the distinction is what keeps a
// run whose scrapes were failing from publishing a fleet that looks idle.
type BatchOccupancy struct {
	Fraction float64 `json:"fraction"`
	Read     bool    `json:"read"`
}

// String renders the reading for a log line, keeping "unread" distinct from a
// percentage.
func (o BatchOccupancy) String() string {
	if !o.Read {
		return "unread"
	}
	return fmt.Sprintf("%.1f%%", o.Fraction*100)
}

// ReadBatchOccupancy scrapes one replica's batch KV occupancy from its /metrics
// URL. A nil client uses http.DefaultClient.
//
// A failure is not an error, for the same reason ReadPrefixCache's is not: the
// caller is routing requests, and a scrape that did not answer must degrade the
// signal rather than the routing.
func ReadBatchOccupancy(ctx context.Context, client *http.Client, metricsURL string) BatchOccupancy {
	body, ok := scrape(ctx, client, metricsURL)
	if !ok {
		return BatchOccupancy{}
	}
	fraction, found := Value(body, BatchKVUsage)
	if !found {
		return BatchOccupancy{}
	}
	return BatchOccupancy{Fraction: fraction, Read: true}
}
