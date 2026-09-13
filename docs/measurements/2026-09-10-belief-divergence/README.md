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
| axis | WS 0.25, 1, 3, 8 at fixed skew 0 | think time 30 s and 75 s at WS 3, skew 1 |
| driver | closed-loop, 32 users | open-loop, 8 req/s |
| cells | 12 (4 points × 3 repetitions) | 6 (2 points × 3 repetitions) |
| **status** | **clean, unflagged** | **every cell flagged — superseded, see below** |

**Two things here were finished by [#29 on 2026-09-12](../2026-09-12-recency-rerun/).** The WS 3
rung was added to the working-set axis, which now has all four of its points; and the recency
axis was re-run at a geometry whose cells do not flag, so **the curve below is superseded by the
published one there**. The six flagged cells and the report drawn from them are kept as the
record of what a fractional measured window measures.

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

**Coverage was complete: 19,391 of 19,391 measured requests on the working-set run carried an
engine account of their prompt.** The two independent accounts of computed prefill agree to 0.5% —
7,730,857 tokens summed off the per-request rows against 7,770,437 off the fleet's own counters
over the same windows — so the per-request figure is measuring what it claims to.

## Result: the index is accurate, and it errs in the safe direction

**99.7% of the tokens the index claimed were really there**, over the 19,391 requests of the clean
working-set run, all four rungs of it. The recency cells below are a separate population and are
kept apart: they are flagged, and the re-run that unflagged them is
[its own measurement](../2026-09-12-recency-rerun/).

The errors run overwhelmingly the harmless way: **17,845 requests under-predicted against 1,531
that over-predicted**. The index forgets blocks the replicas still hold far more often than it
believes in blocks they have dropped. That is the asymmetry ADR-0006 was built around — forfeiting
a match costs a prefill that could have been avoided, where believing a stale one pays the prefill
*anyway* and spends the routing decision on a reason that had stopped being true.

### Against working set ratio

| WS (nominal) | requests | honoured | over-predicted | mean tokens over | under-predicted |
|---|---:|---:|---:|---:|---:|
| 0.25 | 10,626 | 100.0% | 951 | 4 | 9,662 |
| 1 | 3,689 | 98.9% | 317 | 314 | 3,370 |
| 3 | 2,639 | 99.2% | 169 | 246 | 2,470 |
| 8 | 2,437 | 99.5% | 94 | 241 | 2,343 |

The honoured share barely moves, and that is itself the finding: **memory pressure makes the index
wrong in bigger pieces, not more often.** The count of over-predicting requests falls monotonically
across the axis — 951, 317, 169, 94 — while the mean over-prediction jumps roughly 80-fold from WS
0.25 to WS 1, 4 tokens to 314. A fleet that cannot hold its working set does not produce more stale
beliefs; it produces stale beliefs that are worth more when they land.

**WS 3 sharpens that into a step rather than a ramp.** With three points the mean over-prediction
read 4 → 314 → 241 and could have been a curve still climbing between them; the fourth point lands
at 246, so the sequence is 4 → 314 → 246 → 241. Almost all of the movement is between WS 0.25 and
WS 1 — the transition from a fleet that holds its working set to one that does not — and past that
the size of a stale belief is flat. The rung was measured on 2026-09-12 by
[#29](../2026-09-12-recency-rerun/), at this axis's own parameters and with a measured window of
100.9–101.8 s against the other rungs' 100.7–102.0 s, so the four are one measurement.

⚠️ **The WS labels are nominal and they are wrong by a constant factor.** ADR-0007 measures the
generator's declared 4 bytes per token against the 1.66 the engines report, so "WS 1" was really
nearer 2.5. The error is identical across cells, so the shape above is sound and only the axis is
mislabelled.

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
**the curve above is indicative and is not a published result.** It wants a re-run before the decay
is claimed as measured. The working-set run above is clean and carries no such caveat.

✅ **[#29 re-ran it on 2026-09-12](../2026-09-12-recency-rerun/), and the decay is real.** On six
cells carrying no flag of any kind the same shape comes back, with the trough still in the bucket
beneath the 57 s TTL: 99.8% honoured at 1–2 s, 97.6% at 10–30 s, **86.0% at 30 s – 1 m**, and then
98.1% again past the TTL, where the index has dropped the belief and stops claiming. **Read that
measurement rather than this table.**

What the re-run also showed is that the flag here was never mostly about a cold fleet. TTFT in
these cells sawtooths with the workload's own visit period — `TurnsPerSession × think time`, because
the rotation advances the whole conversation pool through the same turn together — and both cells
measured a fractional number of those periods. think 75s's two halves shared no turn index at all,
so no warm-up of any length could have cleared it. The re-run measures two whole periods instead.

🔁 **[#33](https://github.com/yuchia329/kvroute/issues/33) rebuilt the check, and these six cells
now say that themselves.** Re-scored from their own rows, the think 30 s cells still flag as a cold
opening — a longer warm-up really is their fix — while the think 75 s cells report two of their four
turn indices confined to one side of the split, and are flagged as a window holding a fractional
number of visit periods rather than as a fleet that was still warming up:

| cell | periods measured | drift as recorded | drift within each turn index | indices not compared | verdict now |
|---|---:|---:|---:|---:|---|
| think30 r1 / r2 / r3 | 1.88 | +0.936 / +2.311 / +1.933 | +1.035 / +1.505 / +1.578 | 0 | cold opening |
| think75 r1 / r2 / r3 | 1.05 | +3.246 / +4.248 / +0.514 | −0.056 / −0.228 / −0.324 | 2 | fractional visit periods |

So the "51% to 425% slower" above is, at think 75 s, two different workloads being compared rather
than a fleet three times slower at the start. The verdict on this table does not change — it is
still superseded by the re-run — but the reason it was withheld is now recorded correctly. See
[the re-score of all 216 recorded open-loop cells](../2026-09-13-warmup-drift/).

## The node cap, calibrated

ADR-0006 sized the index's node cap to the fleet and said plainly what that argument could not
reach: nothing in it proved a fleet-sized index was the *right* size, only that it was a size
derived from the fleet. This is that check.

```
index occupancy      16,311 of 16,311   — the cap bound, so it is a candidate for what over-predicted
fleet-model cap      16,311
calibrated cap       16,258             — the model scaled by the 99.67% of belief the engines honoured
```

A **0.3% trim**, which is the substantive finding: the modelling decision ADR-0006 could not verify
turns out to have been very nearly right.

**Re-derived on 2026-09-12 once WS 3 joined the axis**, because the clean population it is taken
over changed. It moved from 16,266 to 16,258 — eight nodes, 0.05% — so the fourth point does not
disturb the conclusion, it steadies it.

How, so the figure is reproducible from what is committed here: `divergence.json` is
`cmd/divergence -calibration-out` over the four rungs, and the cap is
`prefix.Calibration.NodeCap()` for the reading in `prefix-calibration-observed.json` — the fleet
model of 16,311 scaled by the honoured share, rounded. `cmd/calibrate` was **not** re-run: it
scrapes a live fleet for the eviction histograms, and scraping a different fleet than the cells
were measured against would have replaced the very TTL this measurement is read under. So the
derived half of that file is the reading the run was driven with, unchanged, and only
`observed_divergence` was replaced. Running the same function over #17's own reading reproduces
its published 16,266 exactly, which is what makes the two comparable.

Calibrated from the **clean working-set cells only**, and that is now a choice about mechanism
rather than about flags. The recency cells are no longer flagged, and folding them in would give
16,082; they are still left out, because what goes unhonoured in them is overwhelmingly the **TTL
expiring by design** — their think times of 30 s and 75 s straddle the 57 s TTL deliberately, and
the trough sits exactly where the TTL predicts. Scaling the *cap* by that would shrink the index by
1.4% to correct something the cap does not cause. The working-set cells hold beliefs that are young
and vary memory pressure instead, which is the mechanism the cap is the lever for.

[`prefix-calibration-observed.json`](prefix-calibration-observed.json) carries it, and
[`divergence.json`](divergence.json) is the reading it was derived from.

⚠️ **It is a second configuration, not a replacement.** The comparison's cells all ran at the
frozen fleet-sized cap, and swapping the calibrated one in would leave that run describing a policy
nobody measured. It is written to its own file for that reason; nothing points at it.

## What is here

Cell records only. The per-request rows behind these figures are hundreds of megabytes and stay on
the box — every figure above is recomputable from them with `cmd/divergence`, which reads only and
needs no fleet and no GPU.
