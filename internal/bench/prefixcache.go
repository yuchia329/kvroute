package bench

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// prefixCacheWindow watches what the fleet's prefix caches serve over a cell's
// measured window.
//
// It exists because the prefix cache hit rate is vLLM's own reported figure —
// ground truth for how much of a prompt the engine did not have to prefill — and
// a policy comparison that reported goodput without it could not say whether a
// policy won by keeping conversations warm or by something else entirely.
//
// The opening reading is deliberately not taken when the cell starts. A cell's
// summary excludes its warm-up slice, so counters read from the cell's first
// instant would cover requests the goodput beside them does not, and the two
// columns of one row would describe two different windows. It is taken when the
// measured window opens instead.
//
// That boundary is this window's own clock plus the warm-up, not the driver's:
// the driver marks a row warm-up against a start it takes a moment later, so the
// two boundaries differ by the setup between them — microseconds against a
// warm-up measured in seconds. The window is therefore a few requests wide at
// worst, and it is stated here rather than claimed away because the whole reason
// this reading is delayed at all is to make the two columns describe one window.
// engineCounters is what one window read off the fleet: the two counter pairs
// that say what the caches answered and what the GPUs had to compute.
//
// They travel together because they are read over one window and answer one
// question between them. A prefix cache hit rate is a share of block queries and
// a recomputed-prefill figure is a count of tokens, and a policy can move one
// without moving the other — which is exactly the disagreement idea.md §1 says
// two published results found, so the table has to be able to show it.
type engineCounters struct {
	Cache   vllmmetrics.PrefixCache
	Prefill vllmmetrics.Prefill
}

type prefixCacheWindow struct {
	done   chan struct{}
	before engineCounters
	// replicas are the /metrics URLs, resolved once so that Stop does not repeat
	// the string work on the path a cell ends on.
	replicas []string
	client   *http.Client
}

// watchPrefixCache begins a window that opens opensIn from now.
//
// A sweep given no replica URLs scrapes nothing and the window reports no
// evidence, which is the honest answer: the harness was never told where the
// replicas are, and a fleet-wide hit rate of zero would be a figure invented
// from that absence.
func watchPrefixCache(ctx context.Context, replicas []string, opensIn time.Duration) *prefixCacheWindow {
	w := &prefixCacheWindow{
		done:     make(chan struct{}),
		replicas: metricsURLs(replicas),
		client:   &http.Client{Timeout: 5 * time.Second},
	}
	go func() {
		defer close(w.done)
		if opensIn > 0 {
			timer := time.NewTimer(opensIn)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		}
		w.before = w.read(ctx)
	}()
	return w
}

// Stop closes the window and returns what the fleet served over it.
func (w *prefixCacheWindow) Stop(ctx context.Context) engineCounters {
	<-w.done
	after := w.read(ctx)
	return engineCounters{
		Cache:   after.Cache.Since(w.before.Cache),
		Prefill: after.Prefill.Since(w.before.Prefill),
	}
}

// read scrapes every replica and pools the readings, so the figure is the
// fleet's rather than one card's. One replica nobody could read makes the whole
// reading unread: a hit rate over a fleet where some replicas were checked and
// some were not is a number nobody can say what is behind.
func (w *prefixCacheWindow) read(ctx context.Context) engineCounters {
	if len(w.replicas) == 0 {
		return engineCounters{}
	}
	cache := make([]vllmmetrics.PrefixCache, 0, len(w.replicas))
	prefill := make([]vllmmetrics.Prefill, 0, len(w.replicas))
	for _, url := range w.replicas {
		cache = append(cache, vllmmetrics.ReadPrefixCache(ctx, w.client, url))
		prefill = append(prefill, vllmmetrics.ReadPrefill(ctx, w.client, url))
	}
	return engineCounters{Cache: vllmmetrics.PoolPrefixCache(cache), Prefill: vllmmetrics.PoolPrefill(prefill)}
}

func metricsURLs(replicas []string) []string {
	urls := make([]string, 0, len(replicas))
	for _, base := range replicas {
		urls = append(urls, strings.TrimSuffix(base, "/")+"/metrics")
	}
	return urls
}
