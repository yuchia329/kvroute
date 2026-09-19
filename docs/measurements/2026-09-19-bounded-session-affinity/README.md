# Bounded session affinity on the pressure grid — 2026-09-19

[#36](https://github.com/yuchia329/kvroute/issues/36): session affinity's consistent-hash ring with
a load bound — consistent hashing with bounded loads, at CacheRoute's ε = 0.25 — run across the
twelve points of #18's pressure grid, and read against the session-affinity and prefix-affinity
cells that grid already holds. It answers the objection the README carried in print: every
published margin was over a ring that is blind to load.

**Prefix affinity still leads at 11 of 12 points, by +5.7% to +31.5%**, outside the repetition
ranges at every one of them. The twelfth is the low-pressure corner, where all three affinity
policies are within spread of each other.

**At the headline point the bound takes 40% of the margin, which is under ADR-0016's trigger.**
At WS 1 / skew 0 session affinity is 8.99, bounded session affinity 11.37 and prefix affinity
14.94 requests per second inside the SLO. The +66.2% over session affinity is +31.5% over bounded
session affinity, and the bound closes 2.38 of the 5.95 gap. ADR-0016 set the README correction to
trigger at more than half, before these cells ran; every bounded repetition (11.19–11.42) sits
below the halfway mark of 11.97. The headline stands, stated as both margins.

**At skew 1.4 the bound takes most of it.** Down that column the bound alone closes 81–93% of the
gap between session affinity and prefix affinity. The +162% to +373% published there is mostly
what one load-balancer setting buys; what the index adds on top is +5.7% to +13.7%. ADR-0016's
trigger is written for the +66% point and does not fire on this, but it is the larger correction
of the two and the README now says it beside the table it corrects.

**At skew 0 above WS 1 the bound buys nothing.** At WS 3 and WS 8 bounded and load-blind session
affinity are within spread (+1.4%, −0.4%), while the bound still deflects 17–19% of turns. With
no skew the ring already lands load evenly; what the bound moves there is noise around the mean.

**The bound is paid for in cache.** It deflects 14–42% of turns, each one sent away from the
replica that served the conversation, and bounded session affinity's prefix cache hit rate is 1–8
points under session affinity's at every point. Against the load-blind ring the README's second
finding — the win is load, not cache reuse, hit rates within 0.0–2.2 points — holds. Against the
bounded ring prefix affinity leads on hit rate by 0.4–8.7 points, so against the harder baseline
part of the index's margin is cache reuse again.

## The numbers

Goodput at 32 users, median of three repetitions. Session affinity and prefix affinity are #18's
cells, recorded 2026-09-10/11 and not re-run.

| WS | skew | session affinity | bounded, ε 0.25 | prefix affinity | prefix over bounded | prefix over session | gap the bound closes | turns deflected | hit rate: session / bounded / prefix |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| 0.25 | 0 | 34.37 | 31.86 | 32.04 | +0.6% (within spread) | −6.8% (within spread) | — | 13.6% | 99.7 / 97.0 / 97.4 |
| 0.25 | 1 | 8.83¹ | 30.55 | 34.43 | +12.7% | +289.8%¹ | 84% | 35.7% | 98.8 / 96.1 / 98.3 |
| 0.25 | 1.4 | 7.36 | 32.93 | 34.79 | +5.7% | +373.0% | 93% | 42.1% | 98.5 / 97.6 / 98.4 |
| 1 | 0 | 8.99 | 11.37 | 14.94 | **+31.5%** | **+66.2%** | **40%** | 20.9% | 76.6 / 68.8 / 77.5 |
| 1 | 1 | 16.96 | 19.80 | 23.51 | +18.7% | +38.6% | 43% | 24.2% | 91.2 / 86.0 / 90.1 |
| 1 | 1.4 | 11.23 | 26.77 | 30.45 | +13.7% | +171.2% | 81% | 40.4% | 96.1 / 94.1 / 96.1 |
| 3 | 0 | 9.32 | 9.44 | 11.61 | +22.9% | +24.6% | 6% | 18.6% | 67.6 / 62.6 / 68.6 |
| 3 | 1 | 12.53¹ | 16.01 | 18.97 | +18.5% | +51.4%¹ | 47% | 22.9% | 85.3 / 80.2 / 84.8 |
| 3 | 1.4 | 6.44 | 25.48 | 28.79 | +13.0% | +346.9% | 85% | 38.7% | 94.5 / 92.8 / 94.8 |
| 8 | 0 | 8.83 | 8.80 | 10.68 | +21.4% | +21.0% | −2% | 17.3% | 64.3 / 59.9 / 65.6 |
| 8 | 1 | 13.49 | 14.69 | 16.91 | +15.1% | +25.3% | 35% | 18.8% | 81.8 / 77.1 / 81.5 |
| 8 | 1.4 | 10.69 | 24.92 | 28.04 | +12.5% | +162.4% | 82% | 34.6% | 93.8 / 92.0 / 94.1 |

¹ Two session-affinity repetitions at these points, as published in
[`2026-09-11-pressure-grid`](../2026-09-11-pressure-grid/).

"Gap the bound closes" is (bounded − session) ÷ (prefix − session), on medians. It is left blank
at the corner where the three are within spread and there is no gap to close. Ranges and spreads
for every figure are in the maps: [prefix over bounded](pressuremap-bounded-vs-prefix.md),
[bounded over session](pressuremap-session-vs-bounded.md), and the [regime map](regimemap.md)
with all five policies. One map per baseline, never one table with an unnamed reference
(ADR-0016). All three are redrawn from the committed cell records:

    pressuremap -baseline bounded_session_affinity -challenger prefix_affinity \
        docs/measurements/2026-09-11-pressure-grid/grid/ws*-skew* \
        docs/measurements/2026-09-19-bounded-session-affinity/grid/ws*-skew*

Session affinity's skew-1.4 cells are bistable (±23% to ±118% spread); bounded session affinity's
are not (±2% to ±7%). Part of what the bound buys at skew 1.4 is that it removes the second state.

## What was held constant

| | |
|---|---|
| policy | `bounded_session_affinity`: session affinity's ring, walked clockwise past any replica whose inflight is at or over ⌈1.25 × (fleet inflight + 1) ÷ replicas⌉, remembering nothing between turns. Supplied session identity, as session affinity is measured |
| bound | ε = **0.25**, CacheRoute's setting for the same policy. Stated on the router, checked by the sweep before the first cell, recorded in every cell's `inflight_bound` column. **No other ε was run** — the objection that 0.25 was a bad choice is open, deliberately (#35) |
| workload, axes, load, SLO, cell length | #18's, to the value: the frozen `multiturn` geometry, WS 0.25 / 1 / 3 / 8 against 629,760 tokens × skew 0 / 1 / 1.4, 32 closed-loop users, TTFT < 990 ms and inter-token p50 < 24 ms, 300 s cells with 50 s warm-up and 10 s settle, 3 repetitions. The workload names match #18's cells byte for byte, which is what lets `pressuremap` put them in one table |
| fleet | five cards, GPUs 0 1 2 4 5 (ADR-0013); KV cache events **off**, as #18's grid ran (ADR-0010); cycled before the grid (ADR-0004); points in `bench.PressureGrid()`'s order on one fleet, as every other policy's were |
| engine | vLLM 0.28.0, unchanged from #18 — [`evidence/versions.env`](evidence/versions.env) |
| binaries | `bench` md5 `f125df73a4cb…`, `router` `8f24920e88b0…`, cross-compiled from a clean checkout of `dc981b1` — [`evidence/md5sums-grid.txt`](evidence/md5sums-grid.txt) |
| driver | [`ops/box/run-bounded-grid.sh`](../../../ops/box/run-bounded-grid.sh), md5 `e330a30aca7c…`, committed before it ran |

Smoke 09:41–09:47 UTC, de-risk 09:47–10:35, grid 10:36–13:52, 3h16m for the twelve points. **No
cell was flagged**: 42 of 42 clean on contamination, warm-up drift and failure rate, none re-run.
No decision was undecided and no session unidentified in any cell.

## The de-risk points, and what they say about order

#36 asked for WS 1 / skew 0 and WS 1 / skew 1.4 before anything else in #35, because they are
the cheapest test of whether the headline survives. They ran first, each on a freshly cycled
fleet, into [`derisk/`](derisk/), and the grid then ran both points again in its own order rather
than resuming them.

| point | de-risk (cold fleet) | grid (in order) |
|---|---:|---:|
| WS 1, skew 0 | 11.53 (11.13–11.68) | 11.37 (11.19–11.42) |
| WS 1, skew 1.4 | 27.65 (27.42–28.07) | 26.77 (26.57–28.35) |

The two agree within their ranges, so the order a point runs in is worth about 1–3% here and not
more. The grid's cells are the ones every table above uses; the de-risk cells are kept as the
record of what was known on day one and are pooled into nothing.

## What this does not establish

- **One ε.** A tighter bound deflects more and balances harder; a looser one approaches the
  load-blind ring. Whether some other ε closes more of the gap was not measured.
- **The baselines are nine days older than the challenger.** Session affinity and prefix affinity
  were recorded 2026-09-10/11 and bounded session affinity on 2026-09-19, on the same cards, engine
  version, engine configuration and SLO, by a newer `bench` and `router`. No same-night control of
  either baseline was run. The agreement between the de-risk and grid cells bounds run-to-run
  drift within a day, not across nine.
- **Supplied identity only.** The derived-identity variant is out of scope (#35).
- **One load rung.** 32 closed-loop users, as the grid has always been. #31 showed the spill rule
  behaves differently at other loads, and nothing here says how the bound does.
- **Not Envoy's, HAProxy's or CacheRoute's implementation.** The rule is the published one and the
  bound is CacheRoute's; the ring, the virtual node count, the hash and what counts as inflight
  are this repo's (ADR-0016).
