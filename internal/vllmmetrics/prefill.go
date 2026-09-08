package vllmmetrics

import (
	"context"
	"fmt"
	"net/http"
)

// Prefill is a replica's prompt-token counters: either one reading of them, or
// the difference between two.
//
// It is the physical measurement the project is about. Prefix cache hit rate is
// a ratio of block queries and says how often the cache answered; this says how
// many prompt tokens the GPU actually had to compute. A policy can lift a hit
// rate and still leave the fleet computing more tokens, and only this pair can
// show that.
//
// vLLM once published a redundant-prefill counter directly and removed it, so
// the figure is derived from the two counters that remain — which is why they
// are read as a pair and never separately.
type Prefill struct {
	// PromptTokens is every prompt token the replica processed:
	// vllm:prompt_tokens_total.
	PromptTokens float64 `json:"prompt_tokens"`
	// CachedTokens is the share of those it answered out of its KV cache
	// instead of computing: vllm:prompt_tokens_cached_total.
	CachedTokens float64 `json:"cached_tokens"`
	// Read is false when the counters could not be scraped, so "computed
	// nothing" and "nobody looked" do not read the same.
	Read bool `json:"read"`
}

// Recomputed is the prompt tokens the replica had to prefill because it did not
// hold them.
//
// This is not yet redundant prefill in CONTEXT.md's sense: that term is
// reserved for work one replica did which another replica already held, and no
// single replica's counters can know what its siblings held. RedundantAgainst
// is where that comparison is made.
func (p Prefill) Recomputed() float64 {
	if !p.Read {
		return 0
	}
	return p.PromptTokens - p.CachedTokens
}

// RedundantAgainst is the redundant prefill this reading carries over a
// baseline: the extra prompt tokens computed, on the same bytes.
//
// The comparison is what makes it redundant rather than merely computed. Every
// policy in the sweep sends the identical workload — the harness refuses to
// compare cells whose workloads differ — so a policy that leaves the fleet
// computing more prompt tokens than another did so by scattering conversations
// across replicas that had to prefill what a sibling was already holding. That
// difference is the physical work routing can remove, measured rather than
// inferred from the router's own beliefs.
func (p Prefill) RedundantAgainst(baseline Prefill) (float64, bool) {
	if !p.Read || !baseline.Read {
		return 0, false
	}
	return p.Recomputed() - baseline.Recomputed(), true
}

// CacheRate is the share of prompt tokens the replica did not have to compute.
//
// It is a token share, where PrefixCache.HitRate is a block-query share. The two
// answer different questions and routinely disagree, so they are reported side
// by side rather than one standing in for the other.
func (p Prefill) CacheRate() float64 {
	if !p.Read || p.PromptTokens <= 0 {
		return 0
	}
	return p.CachedTokens / p.PromptTokens
}

// Evidenced reports whether this reading says anything about prefill at all. A
// window in which no prompt token was processed is read and empty, and a table
// printing 0% for it would report an idle fleet as a fleet that cached nothing.
func (p Prefill) Evidenced() bool { return p.Read && p.PromptTokens > 0 }

// String renders the reading as the work done and the work avoided, because
// either alone is unreadable: a large recompute is expected of a large window.
func (p Prefill) String() string {
	if !p.Read {
		return "unread"
	}
	return fmt.Sprintf("%.0f recomputed of %.0f (%.1f%% cached)", p.Recomputed(), p.PromptTokens, p.CacheRate()*100)
}

// Since returns what the replica prefilled between two readings.
//
// A counter that went backwards means the replica restarted inside the window,
// which is not a delta: reporting one would publish a fraction of a lifetime the
// window never measured.
func (after Prefill) Since(before Prefill) Prefill {
	if !before.Read || !after.Read {
		return Prefill{}
	}
	d := Prefill{
		PromptTokens: after.PromptTokens - before.PromptTokens,
		CachedTokens: after.CachedTokens - before.CachedTokens,
		Read:         true,
	}
	if d.PromptTokens < 0 || d.CachedTokens < 0 {
		return Prefill{}
	}
	return d
}

// PoolPrefill adds up several readings, so a fleet-wide figure carries the
// evidence of every replica behind it. One unread reading makes the whole pool
// unread: the quantity is fleet-wide prefill work, and a sum missing a replica
// is not a smaller measurement of it but a different one.
func PoolPrefill(readings []Prefill) Prefill {
	if len(readings) == 0 {
		return Prefill{}
	}
	pooled := Prefill{Read: true}
	for _, r := range readings {
		if !r.Read {
			return Prefill{}
		}
		pooled.PromptTokens += r.PromptTokens
		pooled.CachedTokens += r.CachedTokens
	}
	return pooled
}

// PromptTokens and PromptTokensCached are the counters redundant prefill is
// derived from. Named here for the reason the package exists: a dependency on a
// metric belongs in Required, where the contract test asserts it against a live
// replica.
const (
	PromptTokens       = "vllm:prompt_tokens_total"
	PromptTokensCached = "vllm:prompt_tokens_cached_total"
)

// ReadPrefill scrapes one replica's prompt-token counters from its /metrics
// URL. A nil client uses http.DefaultClient.
//
// A failure is not an error, for the same reason it is not one in
// ReadPrefixCache: the caller measured whatever it measured, and the record says
// its prefill evidence is missing rather than losing the measurement over a
// scrape that did not answer.
func ReadPrefill(ctx context.Context, client *http.Client, metricsURL string) Prefill {
	body, ok := scrape(ctx, client, metricsURL)
	if !ok {
		return Prefill{}
	}
	return ReadPrefillFrom(body)
}

// ReadPrefillFrom reads the counters out of a Prometheus text exposition.
//
// Both or neither: a reading with one of the pair is not a smaller reading, it
// is one from which recomputed prefill cannot be derived at all.
func ReadPrefillFrom(body string) Prefill {
	prompt, promptOK := Value(body, PromptTokens)
	cached, cachedOK := Value(body, PromptTokensCached)
	if !promptOK || !cachedOK {
		return Prefill{}
	}
	return Prefill{PromptTokens: prompt, CachedTokens: cached, Read: true}
}
