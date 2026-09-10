package fakereplica

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// Bucket bounds are deliberately short. The fake's job is to expose the same
// metric families under the same names as the engine, with internally
// consistent values; it is not trying to reproduce the engine's boundaries.
var (
	latencyBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 30}
	tokenBuckets   = []float64{16, 64, 256, 1024, 4096, 16384}
)

// histogram is a Prometheus histogram: per-bucket counts rendered cumulatively,
// plus a sum and a count that agree with them.
type histogram struct {
	bounds []float64
	counts []int64 // one per bound, plus a final +Inf bucket
	sum    float64
	count  int64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{bounds: bounds, counts: make([]int64, len(bounds)+1)}
}

func (h *histogram) observe(v float64) {
	h.counts[bucketIndex(h.bounds, v)]++
	h.sum += v
	h.count++
}

// bucketIndex is the bucket v falls in: the first bound it is less than or
// equal to, or the trailing +Inf bucket.
func bucketIndex(bounds []float64, v float64) int {
	i, _ := slices.BinarySearch(bounds, v)
	return i
}

func (h *histogram) render(b *strings.Builder, name, labels string) {
	var cumulative int64
	for i, bound := range h.bounds {
		cumulative += h.counts[i]
		fmt.Fprintf(b, "%s_bucket{%s,le=\"%g\"} %d\n", name, labels, bound, cumulative)
	}
	fmt.Fprintf(b, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, labels, h.count)
	fmt.Fprintf(b, "%s_sum{%s} %g\n", name, labels, h.sum)
	fmt.Fprintf(b, "%s_count{%s} %d\n", name, labels, h.count)
}

// newHistograms builds one histogram per required histogram family. Families
// the fake does not model stay empty, which is honest exposition: the series
// exist, and report that nothing was observed.
func newHistograms() map[string]*histogram {
	out := map[string]*histogram{}
	for _, f := range vllmmetrics.Required {
		if f.Kind != vllmmetrics.Histogram {
			continue
		}
		bounds := latencyBuckets
		if f.Name == "vllm:request_prefill_kv_computed_tokens" {
			bounds = tokenBuckets
		}
		out[f.Name] = newHistogram(bounds)
	}
	return out
}

// cacheConfigLabels renders the KV cache geometry the capacity reader reads.
// The token count is rendered from the block count rather than stored beside
// it, so the fake cannot publish a pair that disagrees with itself while the
// engine's own pair agrees.
func (r *Replica) cacheConfigLabels() string {
	return fmt.Sprintf("%s=%q,%s=%q,%s=%q,%s=%q",
		vllmmetrics.LabelBlockSize, strconv.Itoa(r.cfg.BlockSize),
		vllmmetrics.LabelNumGPUBlocks, strconv.Itoa(r.cfg.NumGPUBlocks),
		vllmmetrics.LabelKVCacheSizeTokens, strconv.Itoa(r.cfg.NumGPUBlocks*r.cfg.BlockSize),
		vllmmetrics.LabelGPUMemUtilization, "0.9")
}

// handleMetrics exposes every family in vllmmetrics.Required in the Prometheus
// text format, under the engine's own names. The contract test asserts this
// surface against a live replica, so a divergence is caught rather than
// discovered mid-sweep as a column of zeros.
func (r *Replica) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	c := r.counters
	kvUtil := r.cfg.KVUtilization
	// Prefix-cache and preemption counters stay at zero: the fake models no
	// prefix cache yet, and reporting queries without hits would fabricate a
	// real-looking 0% hit rate. A block-LRU cache arrives with the prefix-index
	// work that first depends on these.
	scalars := map[string]float64{
		"vllm:kv_cache_usage_perc": kvUtil,
		// The fake serves every request it accepts immediately, so everything
		// in flight is running and nothing is ever queued. A real engine splits
		// the two at its batch size; the fake models no scheduler and says so
		// by never reporting a queue rather than by inventing one.
		vllmmetrics.NumRequestsRunning:    float64(c.running),
		vllmmetrics.NumRequestsWaiting:    0,
		"vllm:prefix_cache_hits_total":    0,
		"vllm:prefix_cache_queries_total": 0,
		"vllm:prompt_tokens_total":        float64(c.promptTokens),
		"vllm:prompt_tokens_cached_total": float64(c.cachedPromptTokens),
		"vllm:num_preemptions_total":      0,
	}

	var b strings.Builder
	labels := fmt.Sprintf("model_name=%q", r.cfg.Model)
	for _, f := range vllmmetrics.Required {
		fmt.Fprintf(&b, "# HELP %s %s\n", f.Name, f.Help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", f.Name, f.Kind)
		switch {
		case f.Kind == vllmmetrics.Histogram:
			r.histograms[f.Name].render(&b, f.Name, labels)
		case f.Name == vllmmetrics.CacheConfigInfo:
			// An info metric: the value is always 1 and everything it reports
			// is in the labels, so rendering it as a scalar like the others
			// would publish the series and say nothing.
			fmt.Fprintf(&b, "%s{%s} 1.0\n", f.Name, r.cacheConfigLabels())
		default:
			fmt.Fprintf(&b, "%s{%s} %g\n", f.Name, labels, scalars[f.Name])
		}
	}
	r.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
