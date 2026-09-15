# ADR-0006: The prefix index's two bounds are measured, not chosen

**Status:** Accepted · **Date:** 2026-09-08

## Context

The prefix index tracks which replicas are believed to hold which blocks of which conversation.
It is a belief about state the router does not own: the replicas evict on their own schedule, and
nothing tells the router when they do. Two numbers decide how wrong that belief is allowed to get
— how many blocks the index keeps, and how long one entry stands.

Both are easy to write as constants and hard to defend as constants. They are also not symmetric
in their failure modes:

- **Too large, or too long.** The index believes a replica holds a prefix it evicted hours ago and
  sends the request there. That request pays the full prefill *anyway*, and it spent its routing
  decision on a reason that had stopped being true — so it is **strictly worse than having routed
  on load**, which is idea.md §4.3's phrasing and the reason an unbounded index is not a
  conservative choice.
- **Too small, or too short.** The index forgets blocks the replicas are still holding and
  forfeits a match that was really there. That costs a prefill which could have been avoided, but
  it never actively misroutes.

A constant here would sit at the centre of the policy this project's whole claim rests on, and
nothing in the resulting cells would show that it had been picked rather than derived. "The trie
was tuned until it won" is the reading the comparison cannot survive.

vLLM publishes what is needed to derive both, under `--kv-cache-metrics`:
`vllm:kv_block_lifetime_seconds`, `vllm:kv_block_idle_before_evict_seconds` and
`vllm:kv_block_reuse_gap_seconds`. ADR-0001 turns that flag on from the first cell.

## Decision

**1. Both bounds are derived from three measurements, and nothing else.** `prefix.Calibration`
holds aggregate fleet KV capacity, the measured prompt bytes-per-token ratio, and the engines'
own idle-before-evict distribution. It has no defaults, and `Calibration.Config` returns an error
naming whichever measurement is missing rather than substituting a constant for it.

**2. The node cap models the fleet.** `fleet_tokens × prompt_bytes_per_token ÷ 64`, which is the
fleet's KV capacity expressed as the number of 64-byte blocks of prompt it can hold. At the
125,952 tokens a replica this host measures, that is roughly 37,000 nodes across the five cards
the fleet runs on. The figure is not written down anywhere: capacity is summed off every replica's
own `vllm:cache_config_info` at calibration time, never extrapolated from one card and never
carried over from a fleet of a different size, because it moves both between bring-ups of
identical configuration and whenever the fleet's membership changes — as it did when GPU 3 was
left out for thermal throttling.

**3. The TTL is the p90 of idle-before-evict, not the p99.** Idle-before-evict is the right family
because it measures the thing the TTL is a claim about — how long a block sits unused before the
engine drops it — where block lifetime counts a block's whole life including the time it was being
reused. Deriving a TTL from lifetime would believe longest exactly where a conversation was
hottest. The p90 rather than the p99 follows from the asymmetry above: the TTL should sit where
most blocks are still resident, not where nearly all of them are gone.

**3a. `vllm:kv_block_lifetime_seconds` is recorded beside it as the check, not the source.** A TTL
longer than the median lifetime is a calibration that passed its own test while believing in
blocks the fleet could never have been holding, and nothing else in the file would show it.
`Calibration.CheckAgainstLifetime` reports that disagreement and `cmd/calibrate` prints it. It is
a warning rather than a refusal: the two families measure different things and can legitimately
disagree, so the reader is handed the disagreement instead of having it decided for them.

**4. The router refuses to run prefix affinity without a calibration file.** `-prefix-calibration`
is required for that policy and ignored by the others. `cmd/calibrate` writes the file, and the
bounds it derived are logged at startup so the run's own output carries them.

**5. The calibration is measured during a run and carried forward in a file.** The
idle-before-evict histogram is empty until the fleet has actually evicted blocks, and the fleet is
brought down between policy passes, so re-deriving it at each bring-up would produce nothing. The
file is the evidence: a run whose bounds live only in somebody's shell history cannot be defended
later, which is the whole objection to an arbitrary constant.

## Consequences

- Prefix affinity cannot be run against a fleet that has never been under load. In practice the
  three baseline policies sweep first, so there are both cells to measure the bytes-per-token
  ratio from and a histogram with something in it.
- **Do not enable `--kv-cache-metrics` mid-experiment to obtain this reading.** It changes the
  engine configuration every cell is supposed to share and invalidates every completed cell. The
  error `Calibration.TTL` returns when the family is absent says so, because whoever reads it is
  mid-experiment and that is exactly the tempting repair.
- The node cap is a *model* of the fleet, not a measurement of index accuracy. #17 calibrates it
  against observed belief divergence, which is the check this ADR cannot make: nothing here proves
  a fleet-sized index is the right size, only that it is a size derived from the fleet.
- `vllm:kv_block_idle_before_evict_seconds` and `vllm:kv_block_lifetime_seconds` now both have
  consumers, so a version drift that renamed or dropped either fails the contract test rather than
  silently producing an uncalibratable router.
- The interpolated quantile is only as fine as the engine's bucket boundaries. A p90 that lands in
  the trailing `+Inf` bucket is refused rather than rounded down to the last finite bound, because
  a TTL taken from a bound the exposition cannot locate would be wrong in the dangerous direction.
