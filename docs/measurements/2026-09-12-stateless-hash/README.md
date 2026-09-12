# Stateless prefix hash — the weight axis — 2026-09-12

#26's policy: route on a hash of the prompt's leading blocks combined with replica inflight,
holding no index, no belief and no per-session state. This measurement is the axis that policy
*is* — the weighting between its two terms — swept at one pressure point.

**The load term is an escape hatch, and this is what closing it costs.** Goodput traces an
inverted U across the weight: 21.26/s with the hash worth nothing, peaking at **28.29/s** at
weight 4, then collapsing to 7.76/s with the load term switched off entirely. The peak is
**+33.1%** over routing on load alone.

**It does not fail through the metric you would expect.** TTFT *improves* monotonically as the
weight rises — 465 ms → 374 ms at weight 12 — because concentrating hot conversations on the
replica they hash to is exactly what a prefix cache rewards. What breaks is decode: the batch on
the winning replica deepens, per-request token gaps stretch, and at weight 32 the *median*
request's inter-token gap is 27.3 ms against a 24 ms SLO it cleared at 12.5 ms four points
earlier. Every SLO violation in this sweep is inter-token latency. Not one is TTFT, which never
came within 500 ms of its own threshold at any weight.

This is idea.md §5's predicted mechanism — *"consistent hashing has no escape hatch when the hash
lands hot sessions together"* — measured as a continuous knob rather than inferred from a
comparison between two policies.

Every figure below is recomputable from the cell records in `weights/`.

## What was held constant

| | |
|---|---|
| policy | `prefix_hash`, window **16 blocks** (1,024 bytes of prompt, ~644 engine tokens at the measured 1.59 prompt bytes per token) |
| weight axis | 0 / 1 / 4 / 12 / 32 inflight requests per step down the hash's ranking (`bench.HashWeightGrid`) |
| workload | `multiturn` at the frozen comparison's geometry — 4 turns, 448 new prompt tokens a turn, 64 output, 30% of sessions carrying the shared system prompt, 30% branched, `seed=1`, `usage=on` |
| pressure point | **WS 1, skew 1.4** — the load point, where conversations pile up and the balance between the two terms is live. At skew 0 the hash lands traffic evenly by construction and every weight would read alike |
| load | closed loop, 32 virtual users |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| cells | **300 s**, 50 s warm-up, 10 s settle, 3 repetitions |
| fleet | five cards, GPUs 0 1 2 4 5, **cycled between every weight** — each point sends identical bytes, so without a cold fleet the second weight would read the caches the first left warm (ADR-0004) |
| engine | vLLM 0.28.0, `awq_marlin` / `marlin`, block 16, max model length 8192, GPU memory 0.9, prefix caching, KV cache events on — [`evidence/versions.env`](evidence/versions.env) |
| binaries | `bench` md5 `9f6dd1627664…`, `router` `2802c1b5eb0b…`, built at 2259337 |
| driver | [`ops/box/run-hash-grid.sh`](../../../ops/box/run-hash-grid.sh) `weights` |

The sweep ran 2026-09-12 09:23 → 11:13 UTC, about 21 minutes a point including the fleet cycle.
All 15 cells are clean and none is flagged.

## The axis

| weight | goodput (3 reps) | median | vs weight 0 | p90 TTFT | ITL p50 | ITL p95 | SLO violations | deflected |
|---:|---|---:|---:|---:|---:|---:|---:|---:|
| 0 | 21.07 / 21.26 / 21.48 | 21.26 | — | 465 ms | 12.2 ms | 13.4 ms | 3.5% | 80.4% |
| 1 | 26.79 / 26.80 / 27.85 | 26.80 | +26.1% | 424 ms | 12.2 ms | 13.5 ms | 1.5% | 38.4% |
| **4** | 28.09 / 28.29 / 28.90 | **28.29** | **+33.1%** | 390 ms | 12.5 ms | 16.4 ms | **0.7%** | 28.7% |
| 12 | 17.67 / 19.59 / 22.68 | 19.59 | −7.9% | 374 ms | 13.5 ms | 26.2 ms | 16.7% | 14.7% |
| 32 | 7.22 / 7.76 / 15.37 | 7.76 | −63.5% | 386 ms | **27.3 ms** | 32.6 ms | **52.8%** | 0.0% |

Medians over three repetitions. "Deflected" is the share of decisions the load term moved off the
replica the hash ranked first — `HASH_DEFLECTED` against `PREFIX_HASH` in the decision mix.

### Both ends are controls, and both behaved

**Weight 0 is least-outstanding with a hash that decides nothing**, and it can be checked: with
five replicas, the least loaded one should coincide with the hash's first choice about one time in
five by chance alone. It did — 18.5% on the first repetition against a 20% baseline. That is what
makes the deflection column trustworthy at the weights where it matters.

**Weight 32 is the hash with no load term at all**, and the cells prove it rather than assume it:
deflection is exactly **0.0%**, 5,384 decisions to nil. At 32 virtual users no replica can be more
than 32 requests out of line with the fleet minimum, so a weight of 32 can never be outbid. The
axis reaches its own endpoint instead of stopping short of it.

### The pure-hash end is unstable, not merely worse

The repetition spread widens with the weight, and sharply:

| weight | 0 | 1 | 4 | 12 | 32 |
|---|---:|---:|---:|---:|---:|
| spread across 3 reps | 0.41 | 1.06 | 0.81 | 5.01 | 8.15 |

At weight 4 and below, three repetitions of the same cell land within 1 rps of each other. At 32
they span 7.22 to 15.37 — a factor of two. Which conversations happen to hash together stops
averaging out once nothing can move them, so the fleet's outcome becomes a property of the draw.
A single-number summary of the pure-hash end would hide that; it is the second reason to keep a
load term, independent of the first.

## What produced it

`ops/box/run-hash-grid.sh weights`, which runs one point per weight and cycles the fleet between
them. A smoke ran first, on a borrowed fleet, and is the evidence that the window was right before
any of this was measured: 1,061 decisions, `prompt_unhashed` **0** — every rendered prompt this
workload sends filled the 1,024-byte window — and `undecided` 0, so the harness recognised every
reason the router emitted. [`evidence/smoke-console.log`](evidence/smoke-console.log).

The smoke ran with KV cache events **off** and a 60 s cell, which is why its cell lives in its own
directory and appears in no table here: it was asking whether the router reads this workload's
prompts, not what the fleet scores.

## What this does not say

This is the stateless hash against **itself**, at one pressure point. It says nothing yet about
what the prefix index buys over it — that is the grid arm, run at the weight this axis selected
(4) across the full working-set × skew grid, beside #24's `prefix_affinity` and `exact_residency`
in `runs/pressure-kv-events`. Until that lands, the only cross-policy claim supportable from here
is the weight-0 row, which is least-outstanding measured on the same fleet, the same bytes and the
same night as the rest of the axis.

**This is not a reproduction of OpenAI's router.** The mechanism is inferred from their public
documentation — a hash of *"the initial tokens"* plus machine load, with `prompt_cache_key` folded
in as a disambiguator, and their docs explicit that such keys *"influence routing; they do not pin
requests to a machine or guarantee a cache read hit"* — and not from their implementation, which
is not published. The window, the weighting, the ring and the rank ordering are choices made here
(ADR-0012). The 16-block window is in fact shorter than the shortest prefix their cache will hold.
