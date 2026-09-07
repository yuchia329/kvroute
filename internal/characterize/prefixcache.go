package characterize

import (
	"context"
	"fmt"
	"net/http"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// MaxFloorPrefixHitRate is how much of a probe's prompt tokens may come out of
// the replica's prefix cache before the probe stops describing the hardware.
//
// The latency floor is the cost of a prefill. A prompt the replica has already
// seen is not prefilled at all, and on this fleet the difference is not
// marginal: 325 ms of real prefill against 46 ms of cache hit, measured
// 2026-09-06. Five percent is far above the floor's irreducible hit — the chat
// template's first block is cached on every request — and far below anything
// that would move the median.
const MaxFloorPrefixHitRate = 0.05

// PrefixCacheDelta is how much of a measurement's prompt work the replica
// answered out of its prefix cache.
//
// It is recorded per probe because a probe that read the cache instead of
// prefilling looks exactly like a fast replica, and the whole characterization
// rests on telling those apart. Measured from the engine's own counters, which
// are ground truth for it, rather than inferred from the latency it explains.
type PrefixCacheDelta struct {
	Hits    float64 `json:"hits"`
	Queries float64 `json:"queries"`
	// Read is false when the counters could not be scraped, so "no hits" and
	// "nobody looked" do not read the same.
	Read bool `json:"read"`
}

// HitRate is the share of prompt tokens the cache answered. It is derived
// rather than stored, so a record cannot carry a rate that disagrees with the
// counts it came from.
func (d PrefixCacheDelta) HitRate() float64 {
	if !d.Read || d.Queries <= 0 {
		return 0
	}
	return d.Hits / d.Queries
}

// PoolPrefixCache adds up the prefix-cache evidence of the probes that match,
// so a figure computed over several probes carries the evidence of all of them.
//
// One unread probe makes the whole pool unread. A hit rate averaged over probes
// where some were checked and some were not would be a number nobody could say
// what was behind.
func PoolPrefixCache(probes []Probe, matches func(Probe) bool) PrefixCacheDelta {
	pooled := PrefixCacheDelta{Read: true}
	seen := 0
	for _, probe := range probes {
		if !matches(probe) {
			continue
		}
		seen++
		if !probe.PrefixCache.Read {
			return PrefixCacheDelta{}
		}
		pooled.Hits += probe.PrefixCache.Hits
		pooled.Queries += probe.PrefixCache.Queries
	}
	if seen == 0 {
		return PrefixCacheDelta{}
	}
	return pooled
}

// prefixCounters is one reading of a replica's prefix-cache counters.
type prefixCounters struct {
	hits    float64
	queries float64
	read    bool
}

// readPrefixCounters scrapes the counters the delta is computed from. A failure
// is not fatal: the probe still measured what it measured, and the record says
// its cache evidence is missing.
func readPrefixCounters(ctx context.Context, client *http.Client, r fleet.Replica) prefixCounters {
	if client == nil {
		client = http.DefaultClient
	}
	body, err := scrape(ctx, client, r.URL("/metrics", ""))
	if err != nil {
		return prefixCounters{}
	}
	hits, hitsOK := vllmmetrics.Value(body, vllmmetrics.PrefixCacheHits)
	queries, queriesOK := vllmmetrics.Value(body, vllmmetrics.PrefixCacheQueries)
	if !hitsOK || !queriesOK {
		return prefixCounters{}
	}
	return prefixCounters{hits: hits, queries: queries, read: true}
}

// since returns what the replica served between two readings.
func (after prefixCounters) since(before prefixCounters) PrefixCacheDelta {
	if !before.read || !after.read {
		return PrefixCacheDelta{}
	}
	d := PrefixCacheDelta{
		Hits:    after.hits - before.hits,
		Queries: after.queries - before.queries,
		Read:    true,
	}
	// A counter that went backwards means the replica restarted mid-probe,
	// which is not a delta and must not be reported as one.
	if d.Hits < 0 || d.Queries < 0 {
		return PrefixCacheDelta{}
	}
	return d
}

// reason is why this evidence disqualifies a measurement from describing the
// hardware, or empty if it does not.
func (d PrefixCacheDelta) reason(limit float64) string {
	switch {
	case !d.Read:
		return "the replica's prefix-cache counters could not be read, so there is no evidence this measured prefill rather than cache hits"
	case d.Queries == 0:
		// Read, and did not move. A real replica queries its prefix cache for
		// every prompt token, so zero means nothing was served or the replica
		// models no cache — the fake does not, which is why a dry run against
		// fakes cannot establish a floor.
		return "the replica recorded no prefix-cache queries over this window, so there is no evidence it prefilled anything"
	case d.HitRate() > limit:
		return fmt.Sprintf("%.0f%% of prompt tokens came from the replica's prefix cache, over the %.0f%% limit: this measured the cache, not prefill. The replica had already been sent these prompts — restart the fleet, or send bytes it has not seen",
			d.HitRate()*100, limit*100)
	}
	return ""
}
