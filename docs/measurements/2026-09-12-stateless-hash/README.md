# Stateless prefix hash — what the prefix index buys — 2026-09-12

#26's policy: route on a hash of the prompt's leading blocks combined with replica inflight,
holding no index, no belief and no per-session state. It is the control that isolates what the
prefix index buys, and with #24's exact residency above it the comparison reads as a ladder of how
much a router knows about what its replicas hold: **none → believed → exact**.

**The prefix index buys between +5.4% and +25.2% of goodput over a stateless hash of the same
prompt — median +11.0%** — across the 11 of 12 grid points where the two separate at all. Both buy
between +4.8% and +362% over session affinity. So of everything cache-aware routing is worth on
this hardware class, the trie, its calibration, its TTL, its node cap and its divergence
measurement account for roughly a tenth; a hash and a ring holding no state at all account for the
rest.

**What the index buys is prefix cache hit rate, and the two move together.** Where the index holds
4–6 percentage points more hit rate than the hash it wins 18–25% of goodput; where it holds none it
wins nothing. That is the mechanism, measured rather than argued — see
[Why the index wins where it does](#why-the-index-wins-where-it-does).

**Exact residency does not sit above the index**, and it sits below the stateless hash at low
pressure. Its index was verifiably complete — zero lost batches, zero stream resets, 0.26%
orphaned runs across 885,071 applied events, every prompt tokenized in time — and it still carries
the highest p90 TTFT of the three cache-aware policies at 10 of 12 points, 52–339 ms above the
index at every one of the twelve.

The round-trip is not the whole of it, and this grid was not built to say so. #24 separated the
two terms and found the larger one to be placement rather than cost: exact residency reaches a
*lower* prefix cache hit rate than the believed index and makes the engines compute 12.7% more new
tokens per request, because the index records what the router *sent* and herds concurrent requests
sharing a new prefix onto one replica, while exact residency knows only what has been *reported*
and scatters them until the first prefill lands. A belief is predictive and a fact is not. See
[the exact residency measurement](../2026-09-12-exact-residency/) for that account. This document's
contribution is the rung beneath it, not the explanation of the rung above.

**The stateless hash is the steadiest policy on the grid.** Median spread across three
repetitions: 3.2% for the hash, 3.4% for the index, 4.9% for exact residency, **27.1%** for session
affinity, whose worst point spans 157.8%. A policy with no state matches the index's stability
while session affinity swings between two states.

Every figure below is recomputable from the cell records in `weights/` and `grid/`.

## What was held constant

Both arms share the frozen comparison's geometry. The grid arm additionally matches #24's grid
cell for cell, which is what lets the four policies be read as one ladder.

| | |
|---|---|
| policy | `prefix_hash`, window **16 blocks** — 1,024 bytes of prompt, ~644 engine tokens at the measured 1.59 prompt bytes per token |
| weight | **4** inflight requests per step down the hash's ranking, selected by the weight axis below |
| workload | `multiturn` at the frozen geometry — 4 turns, 448 new prompt tokens a turn, 64 output, 30% of sessions carrying the shared system prompt, 30% branched, `seed=1`, `usage=on` |
| load | closed loop, 32 virtual users |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| cells | **300 s**, 50 s warm-up, 10 s settle, 3 repetitions |
| fleet | five cards, GPUs 0 1 2 4 5 — GPU 3 is out (#25) |
| engine | vLLM 0.28.0, `awq_marlin` / `marlin`, block 16, max model length 8192, GPU memory 0.9, prefix caching, **KV cache events on** — [`evidence/versions.env`](evidence/versions.env) |
| binaries | `bench` md5 `9f6dd1627664…`, `router` `2802c1b5eb0b…`, built at 2259337 |
| driver | [`ops/box/run-hash-grid.sh`](../../../ops/box/run-hash-grid.sh) |

The grid arm ran 2026-09-12 11:15 → 14:31 UTC, 3h11m for 12 points; the weight axis 09:23 → 11:13,
about 21 minutes a point including a fleet cycle. **All 51 cells are clean and none is flagged.**

#24's three arms were recorded by an earlier binary. The complete difference between the two
builds' summary code is 21 added lines for #27's `Placement` field — nothing that computes goodput,
TTFT, inter-token latency, SLO violations or the prefill counters changed, and `result.go` and
`stream.go` are identical. The four arms are therefore directly comparable as written, with no
re-summarising needed.

## The ladder

Median goodput over three repetitions, requests per second meeting the SLO.

| point | session (none, sticky) | **hash (none)** | **index (believed)** | **exact** | hash v session | index v hash |
|---|---:|---:|---:|---:|---:|---:|
| WS 0.25 / skew 0 | 34.53 | 34.34 | 32.15 | 30.00 | −0.5% | −6.4% *(within spread)* |
| WS 0.25 / skew 1 | 10.19 | 32.87 | 34.66 | 31.69 | +222.8% | +5.4% |
| WS 0.25 / skew 1.4 | 7.07 | 32.69 | 35.16 | 32.46 | +362.4% | +7.6% |
| WS 1 / skew 0 | 9.15 | 12.11 | 15.17 | 13.78 | +32.5% | **+25.2%** |
| WS 1 / skew 1 | 17.31 | 21.22 | 23.18 | 21.16 | +22.6% | +9.2% |
| WS 1 / skew 1.4 | 11.02 | 28.45 | 30.63 | 28.34 | +158.0% | +7.7% |
| WS 3 / skew 0 | 8.97 | 9.70 | 11.64 | 10.45 | +8.1% | **+20.1%** |
| WS 3 / skew 1 | 13.50 | 16.93 | 19.02 | 17.39 | +25.4% | +12.4% |
| WS 3 / skew 1.4 | 6.55 | 26.64 | 28.95 | 27.60 | +306.5% | +8.7% |
| WS 8 / skew 0 | 8.71 | 9.12 | 10.76 | 9.30 | +4.8% | **+17.9%** |
| WS 8 / skew 1 | 13.76 | 14.86 | 17.42 | 15.08 | +8.0% | +17.2% |
| WS 8 / skew 1.4 | 10.37 | 25.42 | 28.23 | 26.46 | +145.2% | +11.0% |

Eleven of twelve points separate: the two policies' repetition ranges are disjoint. The exception
is WS 0.25 / skew 0, the low-pressure corner where #18 already found prefix affinity within spread
of session affinity — there is no locality problem there to solve, and carrying an index is
neither help nor harm.

**The index never loses where the two separate.** Its margin is smallest at the lowest working set
and grows with working set in every skew column: +5.4 → +9.2 → +12.4 → +17.2 down the skew-1
column, +7.6 → +7.7 → +8.7 → +11.0 down skew 1.4. The skew-0 column is the exception in shape,
peaking at WS 1 (+25.2%) and easing to +17.9% at WS 8, where so little survives eviction that
every policy sinks toward one floor.

## Why the index wins where it does

Prefix cache hit rate, as the replicas themselves reported it — the engine-side figure, not the
router's prediction of it:

| point | session | hash | index | exact | index − hash |
|---|---:|---:|---:|---:|---:|
| WS 0.25 / skew 0 | 99.7% | 98.3% | 97.1% | 96.3% | −1.2 pp |
| WS 0.25 / skew 1 | 98.9% | 98.0% | 98.4% | 97.1% | +0.4 pp |
| WS 0.25 / skew 1.4 | 98.4% | 98.2% | 98.5% | 97.8% | +0.4 pp |
| WS 1 / skew 0 | 76.8% | 71.7% | 77.6% | 76.6% | **+5.9 pp** |
| WS 1 / skew 1 | 90.9% | 88.0% | 89.9% | 88.5% | +2.0 pp |
| WS 1 / skew 1.4 | 96.2% | 95.0% | 96.2% | 95.1% | +1.2 pp |
| WS 3 / skew 0 | 67.3% | 64.0% | 68.6% | 68.1% | **+4.6 pp** |
| WS 3 / skew 1 | 85.4% | 82.0% | 84.7% | 83.5% | +2.7 pp |
| WS 3 / skew 1.4 | 94.3% | 93.8% | 94.9% | 94.3% | +1.1 pp |
| WS 8 / skew 0 | 64.1% | 61.5% | 65.5% | 65.1% | **+4.0 pp** |
| WS 8 / skew 1 | 81.7% | 78.3% | 81.7% | 80.1% | +3.5 pp |
| WS 8 / skew 1.4 | 94.0% | 92.6% | 94.2% | 93.3% | +1.5 pp |

The hit-rate column and the goodput column move together. The three largest hit-rate gaps — +5.9,
+4.6 and +4.0 pp, all at skew 0 with a working set at or above capacity — are exactly the three
largest goodput margins: +25.2%, +20.1% and +17.9%. Where the gap is nil, at WS 0.25, the margin
is nil or near it.

That is the index earning its complexity on one specific thing: **knowing what has been evicted.**
A stateless hash assumes a replica that ever computed a prefix still holds it. At WS 0.25 that
assumption is simply true — the hash sustains 98% hit rate — and the index is redundant. At WS 8
the hash's hit rate falls to 61.5% while the index holds 65.5%, because the index has a TTL and a
node cap calibrated against the fleet's own eviction tail and the hash has no model of eviction at
all. Under skew the gap narrows again, because a handful of hot conversations take most of the
draws, they are always recently used and therefore always resident, and guessing they are resident
is as good as knowing.

### Session affinity has the best hit rate and the worst goodput

Session affinity's hit rate is **higher than the stateless hash's at all twelve points** — and its
goodput is lower at eleven of twelve. That reproduces the published finding idea.md §5 says to
predict against: Anyscale's load-aware router beat consistent hashing on tail latency *despite a
lower prefix cache hit rate*, and CacheRoute found sticky routing took the highest hit rate and the
lowest capacity. This grid says the same thing from a third direction. Hit rate is not the
objective; it is one term traded against balance, and a policy optimising it alone loses.

## The weight axis

Five weights at WS 1 / skew 1.4 — the load point, where conversations pile up and the balance
between the two terms is live. The fleet was cycled between every weight: each point sends
identical bytes, so without a cold fleet the second would read the caches the first left warm
(ADR-0004).

| weight | goodput (3 reps) | median | vs weight 0 | p90 TTFT | ITL p50 | ITL p95 | SLO violations | deflected |
|---:|---|---:|---:|---:|---:|---:|---:|---:|
| 0 | 21.07 / 21.26 / 21.48 | 21.26 | — | 465 ms | 12.2 ms | 13.4 ms | 3.5% | 80.4% |
| 1 | 26.79 / 26.80 / 27.85 | 26.80 | +26.1% | 424 ms | 12.2 ms | 13.5 ms | 1.5% | 38.4% |
| **4** | 28.09 / 28.29 / 28.90 | **28.29** | **+33.1%** | 390 ms | 12.5 ms | 16.4 ms | **0.7%** | 28.7% |
| 12 | 17.67 / 19.59 / 22.68 | 19.59 | −7.9% | 374 ms | 13.5 ms | 26.2 ms | 16.7% | 14.7% |
| 32 | 7.22 / 7.76 / 15.37 | 7.76 | −63.5% | 386 ms | **27.3 ms** | 32.6 ms | **52.8%** | 0.0% |

"Deflected" is the share of decisions the load term moved off the replica the hash ranked first —
`HASH_DEFLECTED` against `PREFIX_HASH` in the decision mix.

**The load term is an escape hatch, and this is what closing it costs.** Goodput peaks at weight 4,
+33.1% over routing on load alone, and the peak's range (28.09–28.90) is disjoint from second
place, so the selection needed no tie-break.

**It does not fail through the metric you would expect.** TTFT *improves* monotonically as the
weight rises, 465 → 374 ms, because concentrating hot conversations where they hash is exactly what
a prefix cache rewards. Decode is what breaks: the batch on the winning replica deepens,
per-request token gaps stretch, and at weight 32 the *median* request's inter-token gap is 27.3 ms
against a 24 ms bar it cleared at 12.5 ms four points earlier. **Every SLO violation in this sweep
is inter-token latency. Not one is TTFT**, which never came within 500 ms of its own threshold at
any weight.

That is idea.md §5's *"consistent hashing has no escape hatch when the hash lands hot sessions
together"*, measured as a continuous knob rather than inferred from a comparison between two
policies — and it is the same escape hatch the spill rule gives policy 4.

### Both ends are controls, and both behaved

**Weight 0 is least-outstanding with a hash that decides nothing**, and it can be checked: with
five replicas the least loaded one should coincide with the hash's first choice about one time in
five by chance alone. It did — 18.5% against a 20% baseline. That is what makes the deflection
column trustworthy at the weights where it matters.

**Weight 32 is the hash with no load term at all**, and the cells prove it rather than assume it:
deflection is exactly **0.0%**, 5,384 decisions to nil. At 32 virtual users no replica can be more
than 32 requests out of line with the fleet minimum, so a weight of 32 can never be outbid.

### The pure-hash end is unstable, not merely worse

| weight | 0 | 1 | 4 | 12 | 32 |
|---|---:|---:|---:|---:|---:|
| spread across 3 reps | 0.41 | 1.06 | 0.81 | 5.01 | 8.15 |

At weight 4 and below, three repetitions land within 1 rps of each other. At 32 they span 7.22 to
15.37 — a factor of two. Which conversations happen to hash together stops averaging out once
nothing can move them, so the fleet's outcome becomes a property of the draw. That is a second
reason to keep a load term, independent of the first.

## What produced it

`ops/box/run-hash-grid.sh`, in three passes: `smoke`, then `weights`, then `grid`.

The smoke ran first, on a borrowed fleet, and is the evidence that the window was right before
anything was measured: 1,061 decisions, `prompt_unhashed` **0**, `undecided` 0. It ran with KV
cache events off and a 60 s cell, which is why its cell lives in its own directory and appears in
no table here — it was asking whether the router reads this workload's prompts, not what the fleet
scores. [`evidence/smoke-console.log`](evidence/smoke-console.log).

Across the grid arm's 36 cells the decision mix is **168,867 `PREFIX_HASH` and 37,501
`HASH_DEFLECTED`** — 206,368 decisions, of which **zero** were `PROMPT_UNHASHED` and zero
`UNDECIDED`. The 16-block window fitted every prompt this workload sent, at every point of the
grid, and the harness recognised every reason the router emitted.

The map `pressuremap` drew from these cells is in [`pressuremap-hash.md`](pressuremap-hash.md), with
the hash as baseline and the index as challenger.

## What this does not say

**The working-set labels are what each cell was configured for, not what it applied.** Skew
discounts working set — concentrating the draws touches fewer distinct conversations — and a cell
of finite length cannot touch a pool bigger than its visit count. So realised pressure rises more
slowly than the labels, especially at the top of the axis and to the right. Neither discount is
corrected, because correcting either would change the workload's name and refuse every cell
recorded under the old one (ADR-0004, ADR-0007). The flat top end of the skew-1.4 column should be
read with that in mind.

**One concurrency, one cell length, one fleet.** 32 virtual users on five RTX 3090s with 126k
tokens of KV each. The +5.4% to +25.2% band is what the index buys *on this hardware class*; a
fleet with far more aggregate KV would sit nearer the WS 0.25 corner, where the index buys nothing,
and a fleet with far less would sit past WS 8, where nothing survives for either policy to find.

**Exact residency's result is not mine to explain.** Its streams were complete, so nothing here
argues against KV-event-driven residency as a mechanism, and the grid arm this document reports was
not designed to separate its tokenize cost from its placement behaviour. #24 did that separation
and found placement to be the larger term. What this document supports is narrower: that on this
grid exact residency lands below the believed index everywhere, and below a stateless hash wherever
memory pressure is low.

**This is not a reproduction of OpenAI's router.** The mechanism is inferred from their public
documentation — a hash of *"the initial tokens"* plus machine load, with `prompt_cache_key` folded
in as a disambiguator, and their docs explicit that such keys *"influence routing; they do not pin
requests to a machine or guarantee a cache read hit"* — and not from their implementation, which is
not published. The window, the weighting, the ring and the rank ordering are all choices made here
(ADR-0012). The 16-block window is in fact shorter than the shortest prefix their cache will hold.
