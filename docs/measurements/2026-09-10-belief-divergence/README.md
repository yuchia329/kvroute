# Belief divergence — 2026-09-10

How far the router's prefix index was from what the engines actually held, measured per request.
The router models residency it does not own: the replicas evict on their own schedule and nothing
tells the router when they do, so the index is a belief that decays. This is the price of that
belief, in the engine's own units.

idea.md §1 records that nobody publishes this at any scale. The sticky-versus-cache-aware ablation
has been done three times over at datacentre scale; the accuracy of the approximate index those
routers decide on has not been. That is why the result stands whichever policy wins.

| | working set | recency |
|---|---|---|
| directory | [`working-set/`](working-set/) | [`recency/`](recency/) |
| report | [`working-set.md`](working-set.md) | [`recency.md`](recency.md) |
| axis | WS 0.25, 1, 8 at fixed skew 0 | think time 30 s and 75 s at WS 3, skew 1 |
| driver | closed-loop, 32 users | open-loop, 8 req/s |
| cells | 9 (3 points × 3 repetitions) | 6 (2 points × 3 repetitions) |
| **status** | **clean, unflagged** | **every cell flagged — see below** |

Policy 4 with **spill off** throughout, which is the only population the node cap is calibrated
from: spill diverts exactly the requests that would have tested the index's best-match belief.

## How it is measured

The prediction is the prefix match the router made the decision on, read back off its response
header. The truth is `usage.prompt_tokens_details.cached_tokens` — the engine's own account of what
it did not have to compute for that same request.

`vllm:request_prefill_kv_computed_tokens` carries the same quantity and is a histogram, so it has
no request id and cannot be joined to the prediction it would check. It is used as a window-level
cross-check instead. That field is also null unless the replica was started with
`--enable-prompt-tokens-details`, which `ops/versions.env` now sets and `ops/probe-usage.sh`
verifies before a sweep is trusted.

**Coverage was complete: 16,752 of 16,752 measured requests on the working-set run carried an
engine account of their prompt.** The two independent accounts of computed prefill agree to 0.5% —
5,158,497 tokens summed off the per-request rows against 5,183,567 off the fleet's own counters
over the same windows — so the per-request figure is measuring what it claims to.

## Result: the index is accurate, and it errs in the safe direction

**99.7% of the tokens the index claimed were really there**, over the 16,752 requests of the clean
working-set run. Including the flagged recency cells the figure is 98.7% over 29,712 requests; the
two populations are kept apart below and only the clean one is calibrated from.

The errors run overwhelmingly the harmless way: **15,375 requests under-predicted against 1,362
that over-predicted**. The index forgets blocks the replicas still hold far more often than it
believes in blocks they have dropped. That is the asymmetry ADR-0006 was built around — forfeiting
a match costs a prefill that could have been avoided, where believing a stale one pays the prefill
*anyway* and spends the routing decision on a reason that had stopped being true.

### Against working set ratio

| WS (nominal) | requests | honoured | over-predicted | mean tokens over | under-predicted |
|---|---:|---:|---:|---:|---:|
| 0.25 | 10,626 | 100.0% | 951 | 4 | 9,662 |
| 1 | 3,689 | 98.9% | 317 | 314 | 3,370 |
| 8 | 2,437 | 99.5% | 94 | 241 | 2,343 |

The honoured share barely moves, and that is itself the finding: **memory pressure makes the index
wrong in bigger pieces, not more often.** The mean over-prediction climbs roughly 80-fold from WS
0.25 to WS 1 — 4 tokens to 314 — while the count of over-predicting requests falls. A fleet that
cannot hold its working set does not produce more stale beliefs; it produces stale beliefs that are
worth more when they land.

⚠️ **The WS labels are nominal and they are wrong by a constant factor.** ADR-0007 measures the
generator's declared 4 bytes per token against the 1.66 the engines report, so "WS 1" was really
nearer 2.5. The error is identical across cells, so the shape above is sound and only the axis is
mislabelled. **WS 3 was not run** — the axis has four points and three of them are here.

### Against time since the session was last served

Derived from the rows rather than recorded by the router: a request's age is its own start minus
the end of that session's previous turn in the same cell. It therefore costs the routing path
nothing and is recomputable from the record.

| since last served | requests | honoured | mean tokens over-predicted |
|---|---:|---:|---:|
| 1 s – 2 s | 801 | 99.8% | 230 |
| 5 s – 10 s | 1,034 | 98.8% | 743 |
| 10 s – 30 s | 2,332 | 96.9% | 1,335 |
| 30 s – 1 m | 1,786 | **86.9%** | **1,929** |

This is the belief decaying, and it decays in the shape the TTL predicts: the index's TTL is
measured at **57 s** from the engines' own idle-before-evict tail, and the sharp fall lands in the
bucket beneath it. Blocks the engine has dropped are still believed in until the TTL clears them.

🚩 **Every cell of the recency run is flagged as still warming up** — the first measured half was
51% to 425% slower than the second by TTFT p50, against a 25% threshold. A warming fleet is one
whose caches are still filling, which is exactly the condition that produces under-prediction, so
**the curve above is indicative and is not a published result.** It wants a re-run with a longer
warm-up before the decay is claimed as measured. The working-set run above is clean and carries no
such caveat.

## The node cap, calibrated

ADR-0006 sized the index's node cap to the fleet and said plainly what that argument could not
reach: nothing in it proved a fleet-sized index was the *right* size, only that it was a size
derived from the fleet. This is that check.

```
index occupancy      16,311 of 16,311   — the cap bound, so it is a candidate for what over-predicted
fleet-model cap      16,311
calibrated cap       16,266             — the model scaled by the 99.7% of belief the engines honoured
```

A **0.3% trim**, which is the substantive finding: the modelling decision ADR-0006 could not verify
turns out to have been very nearly right.

Calibrated from the **clean working-set cells only**. The recency cells are flagged, and a flagged
cell is not averaged in without being read first — folding them in would have moved the cap to
16,093 on the strength of a fleet that was still warming up, which is a larger correction resting on
weaker evidence.

[`prefix-calibration-observed.json`](prefix-calibration-observed.json) carries it, and
[`divergence.json`](divergence.json) is the reading it was derived from.

⚠️ **It is a second configuration, not a replacement.** The comparison's cells all ran at the
frozen fleet-sized cap, and swapping the calibrated one in would leave that run describing a policy
nobody measured. It is written to its own file for that reason; nothing points at it.

## What is here

Cell records only. The per-request rows behind these figures are hundreds of megabytes and stay on
the box — every figure above is recomputable from them with `cmd/divergence`, which reads only and
needs no fleet and no GPU.
