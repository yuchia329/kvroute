# Exact residency against the believed index — 2026-09-12

#24's policy: follow the engines' own KV cache events, so the router *knows* which blocks each
replica holds instead of believing what it put there. Run head to head against the approximate
prefix index across the same pressure grid, with session affinity as the baseline both exist to
beat. **This is a replication.** llm-d published the precise-versus-approximate comparison as P90
TTFT **0.54 s precise against 31.1 s approximate** at datacentre scale; this repeats it on one host
of five consumer cards.

**The result inverts.** Exact residency is beaten by the approximate index at **all eleven grid
points where it has a usable cell**, by 4.6% to 13.5% of goodput, and carries higher p90 TTFT at
every one. The twelfth, WS 1 / skew 1, was published as −8.7%; re-judged by the warm-up drift check
[#33](https://github.com/yuchia329/kvroute/issues/33) rebuilt, all three of exact residency's cells
there slowed across their measured windows by 27–45%, and the point is now a hole. On this hardware
class the approximation is not merely adequate — it is better, and the ladder of how much a router
knows reads **believed > exact**.

**Exactness bought nothing, because there was nothing left to buy.** The share of the achievable
gain the approximation keeps is **above 100% at all eight points where a share can be claimed**
(106%–278%). At two more, exact residency's gain over session affinity is inside the run-to-run
spread. The last two lost their share to #33's re-score: WS 1 / skew 1 has no usable exact
residency cell, and WS 1 / skew 1.4 is left with one usable session-affinity repetition, which
cannot say whether exact residency gained anything. They read 152% and 113% as first published. A figure above 100% is not a near miss: it says the approximation already
captures the whole of the gain exact knowledge was supposed to unlock, and then some.

**It lost for two independent reasons, and only one of them was predicted.**

1. **It costs 26 ms per request to know exactly.** The router has no tokenizer, so it asks an
   engine's `/tokenize` before every routing decision (ADR-0010). That round-trip is p50
   **25.96 ms**, p90 37.91 ms, and it is essentially the whole of the policy's router overhead:
   **26.33 ms against the index's 0.38 ms**, a factor of 69. Against a fleet whose TTFT p50 is
   96 ms, that is a quarter of the budget spent before anything is routed.
2. **It also placed requests worse — which was not expected.** Aggregate prefix cache hit rate is
   **0.8973 for exact residency against 0.9088 for the index**, and lower even than session
   affinity's 0.9035. The engines had to compute **12.7% more new tokens per request** under the
   exact policy. Half the deficit is a placement failure, not a tolls-and-fees one.

**The index is accurate; it is the *choice* that is worse.** Exact residency's prediction of what
an engine would reuse matched what it did reuse almost exactly — predicted/reused **1.0008** on
first turns and **0.9972** on later ones, with under-prediction on at most 0.5% of requests. So the
event stream, the chain hashing and the block accounting all did their job. The index correctly
describes the replica it picked. It simply picks a worse one.

**Why: a belief is predictive, and a fact is not.** The prefix index records what the router
*sent* — intent — so several requests arriving at once that share a new prefix are all herded onto
the one replica, and the prefix is computed once. Exact residency records only what an engine has
finished computing and reported. While the first request is still prefilling, the others are told,
correctly, that no replica holds that prefix yet — so they scatter, and the same prefix is computed
on several replicas at once. ADR-0010 named this as its first consequence; the grid priced it.

The turn split is the evidence. Requests are hurt **twice as badly on first turns** (−2.28 pp of
hit rate) as on later ones (−1.03 pp). First turns of different sessions arrive concurrently and
independently, sharing only the system prompt and branch families — exactly the case the belief
herds and the fact scatters. Later turns run sequentially inside one closed-loop session, so the
previous turn has finished and *has* been reported, and the exact index sees it.

| hit rate (aggregate) | first turns | later turns | all |
|---|---:|---:|---:|
| prefix affinity (believed) | **0.8254** | **0.9176** | **0.9088** |
| exact residency | 0.8026 | 0.9073 | 0.8973 |
| session affinity | 0.7950 | 0.9150 | 0.9035 |
| deficit, exact vs believed | **−2.28 pp** | −1.03 pp | −1.15 pp |

**The index was complete, so this is not a cold-start artefact.** Across the twelve points the five
streams lost **zero** batches, reset **zero** times and reconnected **zero** times, with 2,285
orphaned runs out of **885,071 applied events — 0.26%**. Not one prompt fell back to load for want
of a tokenization: `PROMPT_UNTOKENIZED` is **0**, so the 500 ms budget never once bound. Exact
residency also never routed cold at all (0 `COLD` decisions against the index's 2.8%), because the
events always name *some* holder. Its knowledge was more complete than the index's and still worth
less.

## What was held constant

KV cache events are an engine setting, so all three policies were re-measured with them on rather
than read off #18's events-off grid (ADR-0010, amending ADR-0007).

| | |
|---|---|
| policies | `prefix_affinity`, `exact_residency`, `session_affinity` — the first two sharing one `affinityRule`, spill and all |
| workload | `multiturn`, 4 turns, 448 new prompt tokens a turn, 64 output, 30% shared system prompt, 30% branched, `seed=1`, `usage=on` |
| load | closed loop, 32 virtual users |
| grid | working set 0.25 / 1 / 3 / 8 × skew 0 / 1 / 1.4 — #18's grid, to the value |
| spill | `0/2` — KV branch off, load imbalance factor 2, for both valved policies |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| cells | **300 s**, 50 s warm-up, 10 s settle, 3 repetitions |
| fleet | five cards, GPUs 0 1 2 4 5 — GPU 3 is out (#25) |
| engine | vLLM 0.28.0, `awq_marlin` / `marlin`, block 16, max model length 8192, GPU memory 0.9, prefix caching, **KV cache events on**, `buffer_steps` 10,000 |
| driver | [`ops/box/run-exact-grid.sh`](../../../ops/box/run-exact-grid.sh) |

Ran 2026-09-11 23:30 → 2026-09-12 09:09 UTC, 3h10m–3h11m per policy with a fleet cycle between
each. **108 cells. Neither `prefix_affinity` nor `exact_residency` has a single flagged cell.**
Three `session_affinity` cells were discarded for warm-up drift, which is the baseline's known
bistability rather than anything about the comparison.

Before any cell ran, [`ops/probe-tokenize.sh`](../../../ops/probe-tokenize.sh) checked the claim the
whole policy rests on: that `/tokenize` returns the very tokens a chat completion of the same body
prefills. Asked with `return_token_ids`, the engine echoed its prompt's ids and they were **equal**,
not merely equal in count, for all three of the workload's body shapes (395, 467 and 701 tokens).

## The map

Δ goodput, exact residency against the believed index, at 32 users. Negative everywhere it was
measured.

| WS \ skew | 0 | 1 | 1.4 |
|---|---:|---:|---:|
| **0.25** | −6.7% | −8.6% | −7.7% |
| **1** | −9.2% | —¹ | −7.5% |
| **3** | −10.3% | −8.5% | −4.6% |
| **8** | −13.5% | −13.5% | −6.3% |

¹ No usable exact-residency cell since #33's re-score: TTFT p50 45%, 34% and 27% slower late in
the measured window than early, within each turn index, with the fleet keeping up. −8.7% as first
published.

The full per-point figures, the share-of-the-gain table and the spill evidence that the grid
applied the pressure it claims to are in [`pressuremap-exact.md`](pressuremap-exact.md), as it was
drawn before #33 re-judged the cells; it is not regenerated, because its other columns predate
[#30](https://github.com/yuchia329/kvroute/issues/30)'s per-request prefill as well. The pooled
TTFT and hit rate figures below are drawn from every cell's rows, flagged or not, and do not move.

## Cost and overhead

Sampled 1 in 20 from the routers' own records.

| policy | tokenize p50 | router overhead p50 | p90 | p99 |
|---|---:|---:|---:|---:|
| session affinity | — | 0.21 ms | 0.30 ms | 1.12 ms |
| prefix affinity | — | 0.38 ms | 0.56 ms | 0.83 ms |
| **exact residency** | **25.96 ms** | **26.33 ms** | 38.39 ms | 56.60 ms |

Pooled TTFT: p50 96.4 ms → **140.0 ms**, p90 457.5 ms → **734.0 ms**. The tokenize round-trip
explains the p50 gap almost exactly (43.6 ms of gap against 25.96 ms of tokenize, the rest being
the second load the tokenize calls put on the same API servers). It does **not** explain the p90
gap of 276 ms, which is four to seven times the tokenize p90 — that is the queueing cost of the
extra prefill the scattering causes, and it is why this is not only a cost result.

## How this sits against llm-d

llm-d measured P90 TTFT **0.54 s precise against 31.1 s approximate**; here the two are **734 ms
exact against 458 ms approximate**, the other way round. These are set beside each other, not
ranked: a different fleet, model, workload and scale.

The inversion is not a contradiction of llm-d so much as a statement of where its premise holds. A
precise index pays off when a routing miss is expensive — long prompts, prefill measured in
seconds, caches whose contents a router cannot guess. Then a fixed 26 ms to know exactly is
nothing. On five consumer cards serving 448-token turns, the picture inverts on both terms at once:
the miss is cheap, because a believed index already reaches 0.909 hit rate and the cache is not the
binding constraint, while 26 ms is a quarter of the entire TTFT budget. The cost of knowing is
roughly fixed; the value of knowing scales with what a mistake costs. This fleet is small enough
that the two cross over.

## Which is better on this hardware class, and why

**The approximate index, at every point with a usable cell.** It is 4.6%–13.5% better in goodput, 276 ms
better at p90 TTFT, and it gets there while knowing strictly less. Exact residency would need the
tokenization to become free *and* the placement deficit to close before it drew level, and the
second is the harder of the two: it is a consequence of routing on completed fact rather than on
intent, not of any inaccuracy that could be fixed.

The one honest caveat is that the 26 ms is the cost of *this* way of knowing, not of exactness
itself. A router that tokenized in-process would pay perhaps 1–2 ms — which ADR-0010 rejected as
needing cgo or an unofficial chat-template port, and as failing silently when a template byte
drifts. Even granting a free tokenizer, the 12.7% redundant prefill and the 1.15 pp hit-rate
deficit remain, and those are what would still have to be answered.

## What this does not rest on

- The KV high-water branch of the spill rule was never armed (`bench.Chosen`), so the grid cannot
  separate memory pressure from load imbalance. That is **#28**, and it is a scoping loss the grid
  shares with #18's.
- `session_affinity` is bistable at high skew and three of its cells were discarded for warm-up
  drift. It is used here only as the baseline of the share-of-the-gain figure, and two points claim
  no share because of it. #33's re-score set aside one more, at WS 1 / skew 1.4.
- The realised working set is lower than the axis label, and more so to the right — the labels are
  what each cell was configured for, not what it applied. The map carries the full warning.
- The four-policy ladder that places the stateless hash beside these three is #26's
  ([measurement](../2026-09-12-stateless-hash/)). Its cells were recorded by a later binary; the
  summary code differs only by #27's `Placement` field, which computes none of these figures.
