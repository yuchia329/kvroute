package vllmmetrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// PrefixCache is a replica's prefix-cache counters: either one reading of them,
// or the difference between two.
//
// This is vLLM's own reported figure and therefore ground truth for how much of
// a prompt the engine did not have to prefill. It is the quantity CONTEXT.md
// calls prefix cache hit rate, as distinct from prefix match, which is the
// router's prediction of it.
//
// It lives here rather than beside either of its callers because two of them
// need the same answer: the characterization pass reads it to prove a latency
// floor measured prefill rather than a warm cache, and the policy comparison
// reads it per cell as a reported column. Those two packages already depend on
// each other in one direction, so a type owned by either would have to be
// duplicated by the other — and a hit rate computed two ways is two numbers.
type PrefixCache struct {
	Hits    float64 `json:"hits"`
	Queries float64 `json:"queries"`
	// Read is false when the counters could not be scraped, so "no hits" and
	// "nobody looked" do not read the same. Reporting an unread replica as a hit
	// rate of zero would put a scrape failure in a published column.
	Read bool `json:"read"`
}

// HitRate is the share of prompt tokens the cache answered. It is derived rather
// than stored, so a record cannot carry a rate that disagrees with the counts it
// came from.
func (p PrefixCache) HitRate() float64 {
	if !p.Read || p.Queries <= 0 {
		return 0
	}
	return p.Hits / p.Queries
}

// Evidenced reports whether this reading says anything at all about a prefix
// cache.
//
// A reading of zero queries is read but empty: a real replica queries its prefix
// cache for every prompt token, so no queries means nothing was served over the
// window or the replica models no cache. Its hit rate is arithmetically zero and
// means nothing, and a table that printed 0% for it would be reporting a cache
// that was never exercised as a cache that never hit.
func (p PrefixCache) Evidenced() bool { return p.Read && p.Queries > 0 }

// String renders the reading as the counts and the rate together, because the
// rate alone cannot say whether it rests on ten queries or ten million.
func (p PrefixCache) String() string {
	if !p.Read {
		return "unread"
	}
	return fmt.Sprintf("%.0f/%.0f (%.1f%%)", p.Hits, p.Queries, p.HitRate()*100)
}

// Since returns what the replica served between two readings.
//
// A counter that went backwards means the replica restarted inside the window,
// which is not a delta: reporting one would publish a fraction of a lifetime the
// window never measured.
func (after PrefixCache) Since(before PrefixCache) PrefixCache {
	if !before.Read || !after.Read {
		return PrefixCache{}
	}
	d := PrefixCache{Hits: after.Hits - before.Hits, Queries: after.Queries - before.Queries, Read: true}
	if d.Hits < 0 || d.Queries < 0 {
		return PrefixCache{}
	}
	return d
}

// PoolPrefixCache adds up several readings, so a fleet-wide figure carries the
// evidence of every replica behind it.
//
// One unread reading makes the whole pool unread, and pooling nothing yields
// nothing. A hit rate averaged over replicas where some were checked and some
// were not is a number nobody can say what is behind.
func PoolPrefixCache(readings []PrefixCache) PrefixCache {
	if len(readings) == 0 {
		return PrefixCache{}
	}
	pooled := PrefixCache{Read: true}
	for _, r := range readings {
		if !r.Read {
			return PrefixCache{}
		}
		pooled.Hits += r.Hits
		pooled.Queries += r.Queries
	}
	return pooled
}

// ReadPrefixCache scrapes one replica's prefix-cache counters from its /metrics
// URL. A nil client uses http.DefaultClient.
//
// A failure is not an error: the caller measured whatever it measured, and the
// record says its cache evidence is missing rather than losing the measurement
// over a scrape that did not answer.
func ReadPrefixCache(ctx context.Context, client *http.Client, metricsURL string) PrefixCache {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return PrefixCache{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return PrefixCache{}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return PrefixCache{}
	}

	hits, hitsOK := Value(string(body), PrefixCacheHits)
	queries, queriesOK := Value(string(body), PrefixCacheQueries)
	if !hitsOK || !queriesOK {
		return PrefixCache{}
	}
	return PrefixCache{Hits: hits, Queries: queries, Read: true}
}
