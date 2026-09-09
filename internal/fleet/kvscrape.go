package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// DefaultKVScrapeInterval is how often the fleet's KV utilization is re-read.
//
// Short, because the signal it feeds is a spill threshold and a reading is at
// worst one interval stale: a replica that crosses its high-water mark keeps
// collecting affinity for the rest of the window. Not shorter, because this is
// one HTTP round trip per replica per tick against the same engines that are
// serving the measurement, and the whole project's numbers come off those
// engines. A quarter second is roughly one scrape per replica per served
// request at the top of the arrival ladder, which is cheap beside a prefill and
// still inside the window a conversation's turns arrive in.
const DefaultKVScrapeInterval = 250 * time.Millisecond

// KVScrapeConfig configures the loop that keeps the fleet's KV utilization
// current. Fleet is required.
type KVScrapeConfig struct {
	Fleet *Fleet
	// Interval is how often every replica is re-read. Zero uses
	// DefaultKVScrapeInterval.
	Interval time.Duration
	// Timeout bounds one replica's scrape. Zero derives it from Interval, so a
	// replica that has stopped answering cannot hold a tick open past the next
	// one: a scraper blocked on one dead replica would leave the whole fleet's
	// readings frozen at whatever they last were, which is the stale-belief
	// failure this loop exists to prevent.
	Timeout time.Duration
	Client  *http.Client
	Log     *slog.Logger
}

func (c KVScrapeConfig) interval() time.Duration {
	if c.Interval <= 0 {
		return DefaultKVScrapeInterval
	}
	return c.Interval
}

func (c KVScrapeConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	// Under the interval rather than equal to it, so a tick's scrapes have
	// finished before the next tick's begin.
	return c.interval() * 4 / 5
}

// ScrapeKVUtilization keeps every replica's KV utilization current until the
// context is done. It blocks, so callers run it in a goroutine.
//
// Every tick writes a reading for every replica, including the ones that did
// not answer. That is the whole of the graceful degradation the spill rule
// rests on, and writing the failure is as important as writing the success: a
// loop that only recorded what it could read would leave a replica that has
// stopped answering believed at the utilization it last reported, and the spill
// rule would go on declining — or failing to decline — on a figure that stopped
// being true. An unread replica instead reads as having no KV signal at all,
// and the policy routes it as it did before the signal existed.
func ScrapeKVUtilization(ctx context.Context, cfg KVScrapeConfig) {
	if cfg.Fleet == nil {
		return
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.timeout()}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}

	ticker := time.NewTicker(cfg.interval())
	defer ticker.Stop()
	// Once before the first tick, so a router that has just come up routes on a
	// real reading rather than on a fleet that is uniformly unread for its first
	// interval.
	cfg.scrapeOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg.scrapeOnce(ctx)
		}
	}
}

// scrapeOnce reads every replica in parallel and records what each said.
//
// In parallel because they are read serially otherwise and a fleet of six with
// one slow replica would take the slow one's latency for all of them, making
// every other reading that much staler than the interval promises.
func (c KVScrapeConfig) scrapeOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	replicas := c.Fleet.Replicas()
	var wg sync.WaitGroup
	for _, r := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reading := vllmmetrics.ReadKVUtilization(ctx, c.Client, MetricsURL(r.BaseURL))
			if err := c.Fleet.ObserveKVUtilization(r.ID, reading); err != nil {
				c.Log.Warn("could not record a KV utilization reading", "replica", r.ID, "err", err)
			}
		}()
	}
	wg.Wait()
}

// MetricsURL is a replica's Prometheus endpoint.
//
// Here rather than spelled at each caller because three of them build it, and a
// fleet scraped at two different paths is a fleet half of whose readings are
// silently missing.
func MetricsURL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/metrics"
}

// Replicas returns the replicas the fleet fronts, so a scraper can walk them
// without taking a whole routing snapshot per tick.
func (f *Fleet) Replicas() []Replica {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]Replica(nil), f.replicas...)
}

// ObserveKVUtilization records what a scrape of one replica found, including
// that it found nothing.
//
// A reading for a replica the fleet does not front is an error rather than a
// discarded write: it means the scraper and the router were pointed at
// different fleets, and a scraper silently writing into nothing would leave
// every replica unread and the spill rule quietly disabled for the whole run.
func (f *Fleet) ObserveKVUtilization(id string, reading vllmmetrics.KVUtilization) error {
	f.mu.RLock()
	i, ok := f.index[id]
	f.mu.RUnlock()
	if !ok {
		return fmt.Errorf("fleet: no replica %q to record a KV utilization reading for", id)
	}
	f.kv[i].Store(&reading)
	return nil
}
