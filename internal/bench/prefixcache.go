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
type prefixCacheWindow struct {
	done   chan struct{}
	before vllmmetrics.PrefixCache
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
func (w *prefixCacheWindow) Stop(ctx context.Context) vllmmetrics.PrefixCache {
	<-w.done
	return w.read(ctx).Since(w.before)
}

// read scrapes every replica and pools the readings, so the figure is the
// fleet's rather than one card's. One replica nobody could read makes the whole
// reading unread: a hit rate over a fleet where some replicas were checked and
// some were not is a number nobody can say what is behind.
func (w *prefixCacheWindow) read(ctx context.Context) vllmmetrics.PrefixCache {
	if len(w.replicas) == 0 {
		return vllmmetrics.PrefixCache{}
	}
	readings := make([]vllmmetrics.PrefixCache, 0, len(w.replicas))
	for _, url := range w.replicas {
		readings = append(readings, vllmmetrics.ReadPrefixCache(ctx, w.client, url))
	}
	return vllmmetrics.PoolPrefixCache(readings)
}

func metricsURLs(replicas []string) []string {
	urls := make([]string, 0, len(replicas))
	for _, base := range replicas {
		urls = append(urls, strings.TrimSuffix(base, "/")+"/metrics")
	}
	return urls
}
