package vllmmetrics

import (
	"context"
	"fmt"
	"net/http"
)

// KVCacheUsage is the gauge saying how full a replica's KV cache is.
//
// It is the one load signal the router cannot derive locally. Inflight is the
// router's own count of what it dispatched, and it says how much work a replica
// has been given; this says how much of the replica's cache that work is
// occupying, which is a different pressure and the one the prefix index's
// beliefs die of. A replica at its high-water mark is evicting, so a match the
// index still believes in there is the belief ADR-0006 calls strictly worse
// than having routed on load.
//
// It is NOT vllm:gpu_cache_usage_perc. The name churned across vLLM releases and
// the engine still publishes both; the other one is not this figure, and a
// scrape that silently matched it would report a number nobody could trace.
const KVCacheUsage = "vllm:kv_cache_usage_perc"

// KVUtilization is one scrape of one replica's KV cache utilization: the
// fraction of its blocks currently allocated.
//
// Read is false when the gauge could not be scraped, so "an empty cache" and
// "nobody could reach this replica" do not read the same. The distinction is
// load-bearing rather than tidy: the spill rule sends declined requests to the
// least-loaded replica, so an unread replica reported as 0.0 would look like the
// emptiest cache in the fleet and collect every request the rule declined
// elsewhere. A scrape failure would then not merely lose a signal, it would
// invert it.
type KVUtilization struct {
	Fraction float64 `json:"fraction"`
	Read     bool    `json:"read"`
}

// Over reports whether this replica is known to be above a high-water mark.
//
// An unread reading is never over, whatever the mark. That is the whole of the
// spill rule's graceful degradation, and it lives here rather than at the call
// site so that a policy cannot forget it: a fleet whose scrapes have stopped
// answering routes as though the KV signal did not exist, which is the policy
// this project measured before the signal was added, rather than spilling every
// request on a reading nobody took.
//
// Strictly over, so a mark of 1.0 cannot fire on a cache that is merely full to
// the last block, and a mark of 0 does not fire on every idle replica.
func (u KVUtilization) Over(highWater float64) bool {
	return u.Read && u.Fraction > highWater
}

// String renders the reading for a log line, keeping "unread" distinct from a
// percentage.
func (u KVUtilization) String() string {
	if !u.Read {
		return "unread"
	}
	return fmt.Sprintf("%.1f%%", u.Fraction*100)
}

// ReadKVUtilization scrapes one replica's KV utilization from its /metrics URL.
// A nil client uses http.DefaultClient.
//
// A failure is not an error, for the same reason ReadPrefixCache's is not: the
// caller is routing requests, and a scrape that did not answer must degrade the
// signal rather than the routing.
func ReadKVUtilization(ctx context.Context, client *http.Client, metricsURL string) KVUtilization {
	body, ok := scrape(ctx, client, metricsURL)
	if !ok {
		return KVUtilization{}
	}
	fraction, found := Value(body, KVCacheUsage)
	if !found {
		return KVUtilization{}
	}
	return KVUtilization{Fraction: fraction, Read: true}
}
