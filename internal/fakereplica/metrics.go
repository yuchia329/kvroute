package fakereplica

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// histogramBuckets is a deliberately short bucket set. The fake's job is to
// expose the same metric families under the same names as the engine, not to
// reproduce its bucket boundaries.
var histogramBuckets = []float64{0.05, 0.5, 5}

// handleMetrics exposes every family in vllmmetrics.Required in the Prometheus
// text format, under the engine's own names. The contract test asserts this
// surface against a live replica, so a divergence here is caught rather than
// discovered mid-sweep as a column of zeros.
func (r *Replica) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	c := r.counters
	kvUtil := r.kvUtil
	r.mu.Unlock()

	values := map[string]float64{
		"vllm:kv_cache_usage_perc":                kvUtil,
		"vllm:num_requests_running":               0,
		"vllm:num_requests_waiting":               0,
		"vllm:prefix_cache_hits":                  float64(c.prefixCacheHits),
		"vllm:prefix_cache_queries":               float64(c.prefixCacheQuerys),
		"vllm:prompt_tokens":                      float64(c.promptTokens),
		"vllm:prompt_tokens_cached":               float64(c.cachedTokens),
		"vllm:num_preemptions":                    0,
		"vllm:request_prefill_kv_computed_tokens": float64(c.promptTokens - c.cachedTokens),
		"vllm:time_to_first_token_seconds":        r.cfg.TTFT.Seconds() * float64(c.requests),
		"vllm:inter_token_latency_seconds":        r.cfg.InterToken.Seconds() * float64(c.completionTokens),
		"vllm:e2e_request_latency_seconds":        0,
		"vllm:kv_block_lifetime_seconds":          0,
		"vllm:kv_block_idle_before_evict_seconds": 0,
		"vllm:kv_block_reuse_gap_seconds":         0,
	}

	var b strings.Builder
	labels := fmt.Sprintf("model_name=%q", r.cfg.Model)
	for _, f := range vllmmetrics.Required {
		fmt.Fprintf(&b, "# HELP %s %s\n", f.Name, f.Help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", f.Name, f.Kind)
		v := values[f.Name]
		switch f.Kind {
		case vllmmetrics.Histogram:
			for _, le := range histogramBuckets {
				fmt.Fprintf(&b, "%s_bucket{%s,le=\"%g\"} %d\n", f.Name, labels, le, c.requests)
			}
			fmt.Fprintf(&b, "%s_bucket{%s,le=\"+Inf\"} %d\n", f.Name, labels, c.requests)
			fmt.Fprintf(&b, "%s_sum{%s} %g\n", f.Name, labels, v)
			fmt.Fprintf(&b, "%s_count{%s} %d\n", f.Name, labels, c.requests)
		default:
			fmt.Fprintf(&b, "%s{%s} %g\n", f.Name, labels, v)
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
