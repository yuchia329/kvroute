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
//
// An alias rather than a type of its own: the policy comparison reads the same
// counters per cell, and this package cannot own the type because the benchmark
// package that also needs it is the one this package imports. The name is kept
// because it is what the characterization record's readers call it.
//
// The underlying type carries either one reading of the counters or the
// difference between two, and both appear here: readPrefixCounters returns a
// reading, and Since turns a pair of them into the delta a probe records. The
// name says "Delta" because that is what reaches the record.
type PrefixCacheDelta = vllmmetrics.PrefixCache

// PoolPrefixCache adds up the prefix-cache evidence of the probes that match,
// so a figure computed over several probes carries the evidence of all of them.
//
// One unread probe makes the whole pool unread. A hit rate averaged over probes
// where some were checked and some were not would be a number nobody could say
// what was behind.
func PoolPrefixCache(probes []Probe, matches func(Probe) bool) PrefixCacheDelta {
	var readings []PrefixCacheDelta
	for _, probe := range probes {
		if matches(probe) {
			readings = append(readings, probe.PrefixCache)
		}
	}
	return vllmmetrics.PoolPrefixCache(readings)
}

// readPrefixCounters takes one reading of the counters a delta is computed from.
// A failure is not fatal: the probe still measured what it measured, and the
// record says its cache evidence is missing.
func readPrefixCounters(ctx context.Context, client *http.Client, r fleet.Replica) vllmmetrics.PrefixCache {
	return vllmmetrics.ReadPrefixCache(ctx, client, r.URL("/metrics", ""))
}

// prefixCacheReason is why this evidence disqualifies a measurement from
// describing the hardware, or empty if it does not.
func prefixCacheReason(d PrefixCacheDelta, limit float64) string {
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
