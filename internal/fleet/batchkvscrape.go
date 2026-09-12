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

// DefaultBatchKVScrapeInterval is how often the fleet's batch KV occupancy is
// re-read.
//
// Short, because the column it fills is read against the router's own inflight
// on the same row, and the two have to describe the same moment for that
// comparison to mean anything: a reading a second old would be compared with an
// inflight count that had turned over. Not shorter, because this is
// one HTTP round trip per replica per tick against the same engines that are
// serving the measurement, and the whole project's numbers come off those
// engines. A quarter second is roughly one scrape per replica per served
// request at the top of the arrival ladder, which is cheap beside a prefill and
// still inside the window a conversation's turns arrive in.
const DefaultBatchKVScrapeInterval = 250 * time.Millisecond

// BatchKVScrapeConfig configures the loop that keeps the fleet's batch KV
// occupancy current. Fleet is required.
type BatchKVScrapeConfig struct {
	Fleet *Fleet
	// Interval is how often every replica is re-read. Zero uses
	// DefaultBatchKVScrapeInterval.
	Interval time.Duration
	// Timeout bounds one replica's scrape. Zero derives it from Interval, so a
	// replica that has stopped answering cannot hold a tick open past the next
	// one: a scraper blocked on one dead replica would leave the whole fleet's
	// readings frozen at whatever they last were, and a frozen column is worse
	// than a missing one because nothing about it looks wrong.
	Timeout time.Duration
	Client  *http.Client
	Log     *slog.Logger
}

func (c BatchKVScrapeConfig) interval() time.Duration {
	if c.Interval <= 0 {
		return DefaultBatchKVScrapeInterval
	}
	return c.Interval
}

func (c BatchKVScrapeConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	// Under the interval rather than equal to it, so a tick's scrapes have
	// finished before the next tick's begin.
	return c.interval() * 4 / 5
}

// ScrapeBatchOccupancy keeps every replica's batch KV occupancy current until
// the context is done. It blocks, so callers run it in a goroutine.
//
// Every tick writes a reading for every replica, including the ones that did
// not answer, and writing the failure is as important as writing the success: a
// loop that only recorded what it could read would leave a replica that has
// stopped answering believed at the occupancy it last reported, and every row
// written after that would carry a figure that had stopped being true.
func ScrapeBatchOccupancy(ctx context.Context, cfg BatchKVScrapeConfig) {
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
func (c BatchKVScrapeConfig) scrapeOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	replicas := c.Fleet.Replicas()
	var wg sync.WaitGroup
	for _, r := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One GET, both signals. They used to be two scrapes of the same
			// endpoint on the same tick, which doubled the request rate against
			// the engines the measurement comes off and made the pair describe
			// two different instants — which is the one thing the correlation
			// between them may not have.
			sample := vllmmetrics.ReadReplica(ctx, c.Client, MetricsURL(r.BaseURL))
			if err := c.Fleet.ObserveReplica(r.ID, time.Now(), sample); err != nil {
				c.Log.Warn("could not record a replica scrape", "replica", r.ID, "err", err)
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

// ObserveBatchOccupancy records what a scrape of one replica found, including
// that it found nothing.
//
// A reading for a replica the fleet does not front is an error rather than a
// discarded write: it means the scraper and the router were pointed at
// different fleets, and a scraper silently writing into nothing would leave
// every row's engine-side load column empty for the whole run.
func (f *Fleet) ObserveBatchOccupancy(id string, reading vllmmetrics.BatchOccupancy) error {
	f.mu.RLock()
	i, ok := f.index[id]
	f.mu.RUnlock()
	if !ok {
		return fmt.Errorf("fleet: no replica %q to record a batch KV occupancy reading for", id)
	}
	f.kv[i].Store(&reading)
	return nil
}

// ObserveReplica records one scrape of one replica: the batch gauge it keeps as
// a column, and the prefix-cache counters its hit rate is differenced from.
//
// Both together, because they came from one response and describe one instant.
// A failed scrape is still recorded for the gauge — an unread reading is what
// keeps a replica that has stopped answering from being believed at what it last
// said — and dropped for the hit rate, where a missing counter is not a data
// point and the window simply spans the gap.
func (f *Fleet) ObserveReplica(id string, at time.Time, sample vllmmetrics.ReplicaSample) error {
	f.mu.RLock()
	i, ok := f.index[id]
	f.mu.RUnlock()
	if !ok {
		return fmt.Errorf("fleet: no replica %q to record a scrape for", id)
	}
	f.kv[i].Store(&sample.BatchKV)
	f.hitRate[i].Observe(at, sample.PrefixCache)
	return nil
}
