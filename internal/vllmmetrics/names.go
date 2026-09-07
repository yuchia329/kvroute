// Package vllmmetrics declares the /metrics surface the router depends on.
//
// Metric names have churned across vLLM releases — the KV-utilization gauge was
// renamed, and a redundant-prefill counter was removed in favour of deriving it
// from two others. The set is therefore declared once, here, and asserted
// against a live replica by the contract test, so a version drift fails loudly
// instead of silently scraping zeros.
package vllmmetrics

// Kind is how a family is exposed in the Prometheus text format, which
// determines the series names a scrape must find.
type Kind int

const (
	Gauge Kind = iota
	Counter
	Histogram
)

func (k Kind) String() string {
	switch k {
	case Gauge:
		return "gauge"
	case Counter:
		return "counter"
	case Histogram:
		return "histogram"
	}
	return "unknown"
}

// Family is one metric the router or the harness reads off a replica.
type Family struct {
	Name string
	Kind Kind
	Help string
}

// SeriesNames returns the series a scrape of this family must find. A histogram
// is exposed as three: bucket, sum and count.
func (f Family) SeriesNames() []string {
	if f.Kind == Histogram {
		return []string{f.Name + "_bucket", f.Name + "_sum", f.Name + "_count"}
	}
	return []string{f.Name}
}

// Required is every metric family this project depends on. The names are taken
// from idea.md §4.6, which records them as read off vLLM 0.28.0 — but nothing
// in this repo has yet confirmed them against a running engine. The contract
// test is what does that, and until it has been run against a live replica
// these names are the spec's claim, not a verified fact.
//
// Adding a dependency on a new metric means adding it here, so that the
// contract test starts checking for it.
var Required = []Family{
	{"vllm:kv_cache_usage_perc", Gauge, "KV-cache usage as a fraction of capacity. NOT gpu_cache_usage_perc."},
	{"vllm:num_requests_running", Gauge, "Requests currently in model execution batches."},
	{"vllm:num_requests_waiting", Gauge, "Requests waiting in the engine queue. Never call this inflight."},
	{"vllm:prefix_cache_hits", Counter, "Prefix-cache block hits. Ground truth for the router's prefix match."},
	{"vllm:prefix_cache_queries", Counter, "Prefix-cache block queries."},
	{"vllm:prompt_tokens", Counter, "Prompt tokens processed."},
	{"vllm:prompt_tokens_cached", Counter, "Prompt tokens served from cache. Redundant prefill is prompt_tokens minus this."},
	{"vllm:request_prefill_kv_computed_tokens", Histogram, "Prefill tokens actually computed. Ground truth for belief divergence."},
	{"vllm:num_preemptions", Counter, "Engine preemptions. This is vLLM's own eviction, never the router's spill."},
	{"vllm:time_to_first_token_seconds", Histogram, "Server-side TTFT, differenced against client-observed TTFT to separate transport from inference."},
	{"vllm:inter_token_latency_seconds", Histogram, "Server-side inter-token latency."},
	{"vllm:e2e_request_latency_seconds", Histogram, "Server-side end-to-end request latency."},
	{"vllm:kv_block_lifetime_seconds", Histogram, "How long a KV block lives, used to calibrate prefix-index TTL."},
	{"vllm:kv_block_idle_before_evict_seconds", Histogram, "How long a KV block sits idle before eviction."},
	{"vllm:kv_block_reuse_gap_seconds", Histogram, "Gap between reuses of a KV block."},
}
