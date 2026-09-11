# Pressure grid — working set × skew, four policies — 2026-09-11

#18's headline figure: the goodput delta between session affinity and prefix affinity across
working set ratio crossed with Zipf skew, at one concurrency, with round robin and least
outstanding beside them for context.

**Prefix affinity is the best policy at 11 of 12 points.** The exception is the low-pressure
corner, WS 0.25 at skew 0, where it is within spread of session affinity — exactly where
idea.md §0 predicted the two would be indistinguishable. As skew rises, session affinity
collapses and swings between two states while prefix affinity holds steady, and the gap
reaches +162% to +373% at skew 1.4 all the way up the working-set axis.

**What this does not yet say is why**, beyond what was measured: see
[What produced it](#what-produced-it). A prefix-affinity arm with the spill rule off, across
the same grid, is running to settle that, and its results will be added here.

Every figure below is recomputable from the cell records in `grid/`.

## What was held constant

| | |
|---|---|
| workload | `multiturn` with the frozen comparison's geometry — 4 turns, 448 new prompt tokens a turn, 64 output, 30% of sessions carrying the shared system prompt, 30% branched, `seed=1`, `usage=on` — at each point's working set and skew |
| working set axis | WS 0.25 / 1 / 3 / 8 against 629,760 tokens of measured fleet KV: 76 / 307 / 922 / 2,460 sessions of 2,048 tokens |
| skew axis | Zipf α 0 / 1.0 / 1.4 |
| load | closed loop, 32 virtual users |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| cells | **300 s**, 50 s warm-up, 10 s settle, 3 repetitions |
| fleet | five cards, GPUs 0 1 2 4 5, cycled between policies so none reads the caches the last one left warm (ADR-0004) |
| engine | vLLM 0.28.0, `awq_marlin` / `marlin`, block 16, max model length 8192, GPU memory 0.9, prefix caching, KV cache metrics and per-request cached tokens on — [`evidence/versions.env`](evidence/versions.env) |
| prefix index | calibrated off the fleet's own eviction tail: TTL 56.97 s derived from 5,076 evictions, not the 20 s fallback; node cap 16,311, fleet-sized |
| spill | `bench.Chosen` — KV high-water **off**, load-imbalance factor 2 — on prefix affinity only |
| binaries | `bench` md5 `78b3f8c6f71e…`, `router` `2e0d0b1078cf…`, built at 88381a2 |
| drivers | [`ops/box/`](../../../ops/box/): `run-pressure-grid.sh`, then `run-pressure-rerun.sh` via `pressure-rerun-watch.sh` |

The grid ran 2026-09-10 20:26 → 2026-09-11 09:17 UTC, 3h11m a policy; the re-run pass
finished at 10:05.

GPU 2 flagged software thermal slowdown on 70% of samples during the run. With load held
equal it runs 0.63 ms slower per token than the fleet-wide fit of inter-token latency against
load share (r = 0.922) — about 5%, and not drifting — so it stayed in, on the same terms GPU 0
did. GPU 3, excluded for a clock drop that grew through a run, is not comparable.

### Why 300 s cells, not the frozen 150 s

ADR-0007 freezes the comparison's cells at 150 s. The grid departs from that on purpose, and
legitimately: every grid point offers its own workload, so no grid cell ever shares a table
with a comparison cell, and within a point every cell has the same length.

The reason was measured rather than assumed. A cell cannot touch more distinct conversations
than it makes session visits, and a 150 s smoke cell made about 2,000 requests — some 500
visits — touching 251 of WS 1's 307 sessions. Carried up the axis, 150 s cells would have left
WS 3 and WS 8 realising 1.26 and 1.48: one point wearing two labels. At 300 s they separate.

## The working set axis as applied

The labels are what each cell was configured for. What it applied is countable from the
session column of the rows — distinct sessions touched, over the 307.5 that fill the fleet —
measured here on session affinity's cells:

| WS \ skew | 0 | 1 | 1.4 |
|---|---:|---:|---:|
| **0.25** | 0.25 | 0.25 | 0.23 |
| **1** | 0.95 | 0.78 | 0.49 |
| **3** | 1.78 | 1.22 | 0.58 |
| **8** | 2.26 | 1.47 | 0.66 |

Skew discounts the working set: concentrating the draws touches fewer distinct conversations,
so the skew-1.4 column spans only 0.23 to 0.66. A policy that serves more requests makes more
visits and touches a little more of its pool, so these are session affinity's figures, not a
single axis for all four. The map's own axis caveat quotes estimates of 0.97 and 0.40 at WS 1;
measured, they are 0.95 and 0.49.

Separately, the generator sizes prompts at 4 bytes a token and the engines report 1.66, so every
label understates true engine tokens by about 2.4× (ADR-0007). In engine tokens the skew-0
column spans roughly 0.6 to 5.4 times the fleet's capacity.

## The map

Δ goodput, prefix affinity against session affinity ([full report](pressuremap.md)):

| WS \ skew | 0 | 1 | 1.4 |
|---|---:|---:|---:|
| **0.25** | −6.8% (within spread) | +289.8%¹ | +373.0% |
| **1** | +66.2% | +38.6% | +171.2% |
| **3** | +24.6% | +41.3% | +346.9% |
| **8** | +21.0% | +25.3% | +162.4% |

¹ **This point rests on two session-affinity repetitions, and its size is not reliable.** One
repetition was flagged for warm-up drift (29%), set aside and re-run, and the re-run drifted
again, harder (43%); it was reported and not retried, and the point was accepted at two. With
two values the pooled median is the lower one — 8.83 of {8.83, 24.61} — so measured against the
higher repetition the delta would be +40%. The point **does** separate either way: session
affinity's best repetition (24.61) sits below prefix affinity's worst (34.20). Read the
direction, not the number. Drift that grows on a re-run looks like this point's two load
states switching mid-cell, which the warm-up check cannot tell from warming, so a longer
warm-up is not expected to settle it.

## All four policies

Goodput — requests per second inside the SLO — median of the usable repetitions:

| WS / skew | round robin | least outstanding | session affinity | prefix affinity |
|---|---:|---:|---:|---:|
| 0.25 / 0 | 5.40 | 11.23 | **34.37** | 32.04 |
| 0.25 / 1 | 9.96 | 18.10 | 8.83¹ | **34.43** |
| 0.25 / 1.4 | 16.27 | 25.35 | 7.36 | **34.79** |
| 1 / 0 | 3.51 | 7.84 | 8.99 | **14.94** |
| 1 / 1 | 6.41 | 13.49 | 16.96 | **23.51** |
| 1 / 1.4 | 11.66 | 21.01 | 11.23 | **30.45** |
| 3 / 0 | 3.24 | 7.57 | 9.32 | **11.61** |
| 3 / 1 | 5.15 | 11.76 | 13.42 | **18.97** |
| 3 / 1.4 | 11.23 | 19.69 | 6.44 | **28.79** |
| 8 / 0 | 3.21 | 7.09 | 8.83 | **10.68** |
| 8 / 1 | 4.74 | 11.05 | 13.49 | **16.91** |
| 8 / 1.4 | 10.21 | 19.44 | 10.69 | **28.04** |

Ranges and spreads are in [`pressuremap.md`](pressuremap.md). Prefix affinity and least
outstanding stay within ±1–7% of their medians everywhere; session affinity ranges from ±11% to
±179%.

There are two regimes. **At skew 0, locality decides it**: the two cache-aware policies lead,
and round robin, which scatters each conversation's turns across the fleet, trails far behind.
**At skew 1.4, balance decides it**: least outstanding, which keeps no conversation together,
comes second, and session affinity falls to the bottom at three of the four working sets —
below even round robin. That is both results idea.md §1 cites: a load-aware router beating
consistent hashing (Anyscale), and sticky routing taking the highest hit rate and the lowest
capacity (CacheRoute). Prefix affinity is the only one of the four that gets locality and
balance at once.

## What produced it

**Not cache reuse.** Between session and prefix affinity the prefix cache hit rate differs by
0.0–2.2 percentage points at every point, and per request they recompute prefill within about
5% of each other at WS ≥ 1. Prefix affinity serves more requests in the same window — 1.1–2.3×
session affinity's at 11 of 12 points — and more of them inside the SLO.

**Load balance, measured.** Session affinity pins each conversation to one replica by hashing
its id, so a hot conversation's every turn lands on one card. At skew > 0, the share of
requests on the busiest replica predicts its goodput at r = −0.797 across 23 cells, spanning
25–65%: for example, 34% on one replica gave 24.61 and 65% gave 5.15. At skew 0 the busiest
replica carries 22–28% — fair share is 20% — and predicts nothing.

Prefix affinity's decisions, pooled per point:

| | skew 0 | skew 1 | skew 1.4 |
|---|---|---|---|
| honoured prefix match | 87.6–97.5% | 92.7–98.8% | 97.7–99.2% |
| cold (no match) | 0.0–12.1% | 0.3–6.2% | 0.5–1.8% |
| spill (match declined) | 0.24–2.51% | 0.93–1.42% | 0.33–0.54% |
| replicas serving each repetition's 3 hottest conversations | 1.8–3.1 | 3.8–4.8 | 4.0–4.3 |

Session affinity serves every conversation from exactly one replica at every point. Prefix
affinity spreads its hot conversations across four or five of the five replicas under skew,
while declining a match on under 1.5% of decisions there. At skew 0 the spread narrows as the
working set grows — 3.1 replicas at WS 0.25 down to 1.8 at WS 8 — while its cold decisions rise
from 0% to 12.1%. That is the same column in which its lead shrinks from +66% to +21%.

**Which part of prefix affinity does the balancing is not settled here.** One reading: a hot
conversation ends up cached on several replicas, its best match ties across them, and the tie
goes to the least-loaded replica (commit 0a80981), so the balancing is tie-breaking. The other:
those replicas were first seeded by the rare spills, which would make the spill rule necessary
however seldom it fires — and #16's spill-off reference at WS 1, skew 1.4 swung between 10.76,
17.05 and 22.01, which points that way. This grid has no spill-off arm, so it cannot tell the
two apart. The arm that can is running.

## Reading the validity table

- **Hit rate spread** in [`pressuremap.md`](pressuremap.md) is worst against best across all
  four policies — 9.7–45.4 pp — so it mostly shows round robin's poor locality. Session against
  prefix alone is the 0.0–2.2 pp above.
- **Redundant prefill** there is absolute recomputed tokens. Under a closed loop the faster
  policy serves more requests, so the column credits the slower policy with having wasted less:
  session affinity is "lowest" at all 12 points, but per request it is lowest at only 6. Do not
  read that column as waste until [#30](https://github.com/yuchia329/kvroute/issues/30)
  normalises it per request.

## Excluded and re-run

§6 discards flagged cells and re-runs them. Six were flagged, all for warm-up drift, and set
aside into their point's `discarded/`:

| policy | point | repetition | drift | after one re-run |
|---|---|---:|---:|---|
| session affinity | WS 0.25, skew 1 | 2 | 29% | drifted again, 43% — excluded¹ |
| session affinity | WS 1, skew 1.4 | 3 | 27% | settled |
| round robin | WS 3, skew 1.4 | 2 | 29% | settled |
| least outstanding | WS 1, skew 0 | 3 | 38% | settled |
| least outstanding | WS 3, skew 0 | 3 | 28% | settled |
| least outstanding | WS 8, skew 0 | 2 | 39% | settled |

Session affinity's re-run at WS 1, skew 1.4 moved that point's delta from +179.5% on two
repetitions to +171.2% on three — the lower-median effect removed.

## What this grid does not answer

#18 asks that memory pressure and load imbalance be separable, since they drive different
branches of the spill rule. **It cannot be answered here.** The KV branch was off:
`vllm:kv_cache_usage_perc` counts blocks held by running requests, so it reads the active batch
rather than cache residency, and on this fleet tracks inflight at r = 0.973. That waits on
[#28](https://github.com/yuchia329/kvroute/issues/28). #18's other six criteria are met.

## Files

| | |
|---|---|
| `pressuremap.md` | The final map, all four policies, drawn after the re-run pass |
| `pressuremap-headline.md` | The same map for session and prefix affinity only, drawn at 02:54 UTC before the context policies ran |
| `grid/ws*-skew*/` | One sweep directory per grid point: `cells/*.json` (144 cell records), `cells.parquet`, `requests.parquet` (every request's row), `results.md`, and `discarded/` (the six set-aside cells' records) |
| `grid/evidence/` | Router startup logs, one per policy, and the re-run pass's |
| `evidence/` | The run's console and bench logs, and the box's `versions.env` |

Kept on the box under `~/kvroute/runs/pressure/` and not committed: each cell's raw row file
(485 MB; the same rows are in `requests.parquet`), the set-aside cells' rows, and the router's
own per-request records (470 MB, 43 MB gzipped), which would double this directory while
largely duplicating what the harness rows already carry.
