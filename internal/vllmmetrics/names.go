// Package vllmmetrics declares the /metrics surface the router depends on.
//
// Metric names have churned across vLLM releases — the KV-utilization gauge was
// renamed, and a redundant-prefill counter was removed in favour of deriving it
// from two others. The set is therefore declared once, here, and asserted
// against a live replica by the contract test, so a version drift fails loudly
// instead of silently scraping zeros.
package vllmmetrics

import (
	"strconv"
	"strings"
)

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

// Required is every metric family this project depends on, verified against a
// live vLLM 0.28.0 replica on 2026-09-06 by test/contract.
//
// The verification corrected idea.md §4.6 in two ways, which is the whole
// reason the assertion exists:
//
//   - Every counter is exposed with a _total suffix. The spec recorded
//     vllm:prompt_tokens; the engine serves vllm:prompt_tokens_total. Gauges
//     and histograms are named as the spec had them.
//   - The three KV residency families are not published by default. They need
//     --kv-cache-metrics, which is off unless asked for; see ADR-0001.
//
// Adding a dependency on a new metric means adding it here, so that the
// contract test starts checking for it.
var Required = []Family{
	{KVCacheUsage, Gauge, "KV-cache usage as a fraction of capacity. The spill rule's signal. NOT gpu_cache_usage_perc."},
	{NumRequestsRunning, Gauge, "Requests currently in model execution batches. This is the engine's actual batch."},
	{NumRequestsWaiting, Gauge, "Requests waiting in the engine queue. Never call this inflight."},
	{PrefixCacheHits, Counter, "Prefix-cache block hits. Ground truth for the router's prefix match."},
	{PrefixCacheQueries, Counter, "Prefix-cache block queries."},
	{PromptTokens, Counter, "Prompt tokens processed."},
	{PromptTokensCached, Counter, "Prompt tokens served from cache. Recomputed prefill is prompt_tokens minus this."},
	{"vllm:request_prefill_kv_computed_tokens", Histogram, "Prefill tokens actually computed. Ground truth for belief divergence."},
	{"vllm:num_preemptions_total", Counter, "Engine preemptions. This is vLLM's own eviction, never the router's spill."},
	{"vllm:time_to_first_token_seconds", Histogram, "Server-side TTFT, differenced against client-observed TTFT to separate transport from inference."},
	{"vllm:inter_token_latency_seconds", Histogram, "Server-side inter-token latency."},
	{"vllm:e2e_request_latency_seconds", Histogram, "Server-side end-to-end request latency."},
	{BlockLifetime, Histogram, "How long a KV block lives. Recorded beside the idle-before-evict tail the prefix-index TTL is taken from, as the sanity check on it."},
	{BlockIdleBeforeEvict, Histogram, "How long a KV block sits idle before eviction. The prefix index's TTL is calibrated off its tail."},
	{"vllm:kv_block_reuse_gap_seconds", Histogram, "Gap between reuses of a KV block."},
	{CacheConfigInfo, Gauge, "The engine's cache configuration. Everything it says is in its labels; see Labels."},
}

// NumRequestsRunning and NumRequestsWaiting are the gauges that say what the
// engine is actually doing, as opposed to what was asked of it.
//
// A driver holding 32 requests against a replica knows 32 are in flight. It
// does not know how many the engine put in a batch and how many are queued
// behind them, and those are different questions: the first is offered load,
// the second is what the hardware is doing with it. Being gauges rather than
// counters, they have to be sampled through a measurement rather than
// differenced across it.
const (
	NumRequestsRunning = "vllm:num_requests_running"
	NumRequestsWaiting = "vllm:num_requests_waiting"
)

// PrefixCacheHits and PrefixCacheQueries are the counters that say how much of
// a measurement's prompt work a replica answered out of its cache rather than
// prefilling.
//
// Named here rather than spelled inline at the one place that reads them, for
// the reason the whole package exists: a dependency on a metric belongs in
// Required, where the contract test asserts it against a live replica, so a
// version drift fails loudly instead of silently reporting a hit rate of zero.
const (
	PrefixCacheHits    = "vllm:prefix_cache_hits_total"
	PrefixCacheQueries = "vllm:prefix_cache_queries_total"
)

// CacheConfigInfo is the series carrying a replica's KV cache geometry.
//
// It is an info metric: its value is always 1 and everything it reports is in
// its labels, so reading capacity off a replica means reading labels rather
// than a value. It is the only place the engine publishes num_gpu_blocks at
// runtime — the startup log says it once and then it is gone — and aggregate
// fleet KV capacity, which every working set ratio scales off, is that number
// summed across the fleet.
const CacheConfigInfo = "vllm:cache_config_info"

// The labels of CacheConfigInfo that capacity is read from. The engine reports
// the token count itself as well as the block count it was derived from, and
// reading both is what lets the derivation be checked rather than assumed.
const (
	LabelNumGPUBlocks        = "num_gpu_blocks"
	LabelBlockSize           = "block_size"
	LabelKVCacheSizeTokens   = "kv_cache_size_tokens"
	LabelGPUMemUtilization   = "gpu_memory_utilization"
	LabelEnablePrefixCaching = "enable_prefix_caching"
)

// Value returns the value of the first series named name, and whether the
// series was there at all. Absent is distinguished from zero: a counter a
// replica does not publish must not read as a counter that has not moved.
func Value(body, name string) (float64, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, raw, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		if base, _, _ := strings.Cut(series, "{"); base != name {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

// Labels returns the label set of the first series named name in a Prometheus
// text exposition, and whether the series was there at all.
//
// Absent is distinguished from empty on purpose: a replica that does not
// publish the series must not read the same as one that publishes it with
// nothing in it.
func Labels(body, name string) (map[string]string, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		_, rest, _ := strings.Cut(line, "{")
		end := strings.LastIndex(rest, "}")
		if end < 0 {
			return nil, false
		}
		return parseLabelSet(rest[:end]), true
	}
	return nil, false
}

// parseLabelSet splits `a="1",b="2"` into its pairs.
//
// It splits on commas outside quotes rather than on every comma: the engine
// publishes list-valued labels such as kv_cache_dtype_skip_layers, and a naive
// split would tear one in half and lose every label after it.
func parseLabelSet(set string) map[string]string {
	labels := map[string]string{}
	quoted := false
	start := 0
	flush := func(pair string) {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return
		}
		labels[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	for i, r := range set {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			flush(set[start:i])
			start = i + 1
		}
	}
	flush(set[start:])
	return labels
}
