# Pressure grid — working set × skew, four policies — 2026-09-11

#18's headline figure: the goodput delta between session affinity and prefix affinity across
working set ratio crossed with Zipf skew, at one concurrency, with round robin and least
outstanding beside them for context.

**Prefix affinity is the best policy at 11 of 12 points.** The exception is the low-pressure
corner, WS 0.25 at skew 0, where it is within spread of session affinity — exactly where
idea.md §0 predicted the two would be indistinguishable. As skew rises, session affinity
collapses and swings between two states while prefix affinity holds steady, and the gap
reaches +162% to +373% at skew 1.4 all the way up the working-set axis.

**Why it wins is the spill rule, together with where new conversations are placed.** A second
arm ran prefix affinity with the spill rule off across the same grid. Without the rule, prefix
affinity serves each hot conversation from a single replica, much as session affinity does, and
loses between 5% and 51% of its goodput at 11 of the 12 points. See
[What produced it](#what-produced-it).

Every figure below is recomputable from the cell records in `grid/` and `spilloff/`.

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
| drivers | [`ops/box/`](../../../ops/box/): `run-pressure-grid.sh`, then `run-pressure-rerun.sh` via `pressure-rerun-watch.sh`; the spill-off arm, `run-pressure-spilloff.sh` |

The grid ran 2026-09-10 20:26 → 2026-09-11 09:17 UTC, 3h11m a policy; the re-run pass
finished at 10:05. The spill-off arm — prefix affinity with the spill rule off, everything else
identical, the same binaries and the same bytes cell for cell — ran 2026-09-11 17:46 → 21:08 UTC
into its own directory, `spilloff/`.

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

### What the spill rule contributes

The grid alone could not say which part of prefix affinity does the balancing. A hot conversation
cached on several replicas has its best match tie across them, and the tie goes to the
least-loaded (commit 0a80981) — but those several replicas might exist only because the rare
spills put them there. The spill-off arm answers it. Goodput, median of three repetitions:

| WS / skew | session affinity | prefix, spill **off** | prefix, spill **on** | spill on vs off |
|---|---:|---:|---:|---:|
| 0.25 / 0 | 34.37 | **35.16** | 32.04 | −8.9% |
| 0.25 / 1 | 8.83¹ | 31.70 | **34.43** | +8.6% |
| 0.25 / 1.4 | 7.36 | 16.89 | **34.79** | +105.9% |
| 1 / 0 | 8.99 | 8.13 | **14.94** | +83.9% |
| 1 / 1 | 16.96 | 20.80 | **23.51** | +13.0% |
| 1 / 1.4 | 11.23 | 19.25 | **30.45** | +58.1% |
| 3 / 0 | 9.32 | 5.97 | **11.61** | +94.3% |
| 3 / 1 | 13.42 | 16.21 | **18.97** | +17.0% |
| 3 / 1.4 | 6.44 | 21.81 | **28.79** | +32.0% |
| 8 / 0 | 8.83 | 6.51 | **10.68** | +64.1% |
| 8 / 1 | 13.49 | 15.98 | **16.91** | +5.8% |
| 8 / 1.4 | 10.69 | 23.66 | **28.04** | +18.5% |

At every point the two prefix arms' repetition ranges do not overlap.

| | session affinity | prefix, spill off | prefix, spill on |
|---|---:|---:|---:|
| replicas serving each repetition's 3 hottest conversations | 1.00 | 1.00–1.67 | 1.78–4.78 |
| busiest replica's share of requests | 22–55% | 23–41% | 21–22% |

**The spill rule is what spreads hot conversations, and it is necessary.** With it off, prefix
affinity serves each hot conversation from one replica at every point — pinned, as session
affinity pins it — and tie-breaking has nothing to choose between. With it on, the busiest
replica sits at fair share (21–22%) at all twelve points, and prefix affinity gains 6–106% at 11
of them. A spill sends a turn of an overloaded conversation to another replica, which then caches
it too; from then on the conversation's prefix match ties across the replicas that hold it, and
the tie goes to the least loaded. The spill makes the choice and tie-breaking takes it — which is
how a rule firing on 0.2–2.5% of decisions accounts for up to a doubling of goodput. It does so
without costing cache hits: with and without it, hit rates match within 1.0 pp everywhere but
WS 0.25 / skew 0.

**That one point is where the rule costs.** At WS 0.25, skew 0 there is no imbalance to fix and
the whole working set fits in cache, so each spill only sends a turn to a replica that does not
hold it: spill on is 8.9% below spill off there, with a hit rate 2.3 pp lower.

**Where a conversation starts matters too, in both directions.** Session affinity places a
conversation by hashing its id; prefix affinity, finding no match for a new conversation, sends it
to the least-loaded replica. With spill off, that placement is the only difference from session
affinity — and it helps under skew (WS 3, skew 1.4: 6.44 → 21.81) but hurts at skew 0 past
WS 0.25 (WS 3, skew 0: 9.32 → 5.97), where the hash was already spreading a uniform draw evenly
and spill-off prefix affinity's busiest replica runs at 28–31% against the hash's 22–26%. Why the
placement unbalances there is not isolated here. The spill rule rescues those cells: spill on is
64–94% above spill off at WS 1, 3 and 8 at skew 0.

Spill-off prefix affinity is also not always steady. At WS 1 / skew 1 and WS 3 / skew 1.4 one of
its three repetitions fell to half the others — 10.55 and 10.78, against about 21. Spill on showed
no such repetition anywhere.

The two arms ran at different times — the spill-on grid overnight, the spill-off arm the next
afternoon — on the same binaries and the same bytes, each from a cold fleet. The effects above are
far larger than either arm's run-to-run spread.

## Reading the validity table

- **Hit rate spread** in [`pressuremap.md`](pressuremap.md) is worst against best across all
  four policies — 9.7–45.4 pp — so it mostly shows round robin's poor locality. Session against
  prefix alone is the 0.0–2.2 pp above.
- **Redundant prefill** there is now recomputed tokens **per request**, with the token total
  printed beside it ([#30](https://github.com/yuchia329/kvroute/issues/30)). It used to be the
  absolute total, and under this closed loop that credited the slower policy: prefix affinity
  served 1.2–2.3× the requests at 11 of the 12 points, so its absolute recomputed prefill rose
  with its own throughput.

### Redundant prefill per request, re-read

The table below is the old column's verdict against the corrected one, for the two affinity
policies. Every figure is recomputed from the cell records in `grid/` — requests from
`summary.requests`, recomputed from `prompt_tokens − prompt_tokens_cached`, usable repetitions
pooled — so this is arithmetic over the existing run, not a re-measurement.

| point | session requests | prefix requests | session / req | prefix / req | lower per request |
|---|---:|---:|---:|---:|---|
| WS 0.25, skew 0 | 25,735 | 24,922 | 10.2 | 78.7 | session |
| WS 0.25, skew 1 | 11,602 | 26,061 | 37.6 | 52.8 | session |
| WS 0.25, skew 1.4 | 13,933 | 26,412 | 44.9 | 48.4 | session |
| WS 1, skew 0 | 8,781 | 11,873 | 722.0 | 695.5 | **prefix** |
| WS 1, skew 1 | 14,577 | 17,867 | 267.3 | 301.4 | session |
| WS 1, skew 1.4 | 14,588 | 23,185 | 117.3 | 117.3 | **prefix**, by 0.001 |
| WS 3, skew 0 | 8,179 | 9,644 | 1,002.9 | 971.9 | **prefix** |
| WS 3, skew 1 | 11,970 | 14,775 | 448.9 | 464.3 | session |
| WS 3, skew 1.4 | 12,188 | 21,824 | 167.6 | 156.6 | **prefix** |
| WS 8, skew 0 | 7,944 | 9,049 | 1,104.4 | 1,062.0 | **prefix** |
| WS 8, skew 1 | 11,534 | 13,379 | 555.4 | 564.9 | session |
| WS 8, skew 1.4 | 12,809 | 21,169 | 186.9 | 179.3 | **prefix** |

Session affinity had the lower absolute recomputed prefill at **all 12** points. Per request it is lower at
**6**, and at WS ≥ 1 the two are within about 5% of each other everywhere. So the old column's
claim — that session affinity wasted less prefill everywhere — was false, and the honest reading is
that the two policies leave the fleet almost the same prefill work per request while prefix
affinity serves 1.2–2.3× as many requests with it.

**None of this map's goodput conclusions rest on that column.** The validity table uses it only to
answer "did the two policies route differently at all", and the answer is unchanged: the spread is
non-zero at every one of the 12 points either way, and the hit rate spread and spill counts beside
it are independent evidence for the same thing. The delta table, the gain share and the
separability section are goodput, which this does not touch.

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

The spill-off arm flagged one cell — prefix affinity at WS 1, skew 1.4, repetition 2, 26% warm-up
drift — and it settled on its single re-run.

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
| `pressuremap-headline.md` | The same map for session and prefix affinity only, drawn at 02:54 UTC before the context policies ran. Not regenerated for [#30](https://github.com/yuchia329/kvroute/issues/30), so its redundant-prefill column is still the absolute one; it says so at the top |
| `grid/ws*-skew*/` | One sweep directory per grid point: `cells/*.json` (144 cell records), `cells.parquet`, `requests.parquet` (every request's row), `results.md`, and `discarded/` (the six set-aside cells' records) |
| `grid/evidence/` | Router startup logs, one per policy, and the re-run pass's |
| `evidence/` | The console and bench logs of the grid and of the spill-off arm, and the box's `versions.env` |
| `spilloff/ws*-skew*/` | The spill-off arm, laid out like `grid/`: prefix affinity's 36 cell records, parquet, results, and the one set-aside cell's record; router startup logs in `spilloff/evidence/` |

Kept on the box under `~/kvroute/runs/pressure/` and `runs/pressure-spilloff/`, and not committed: each cell's raw row file
(485 MB; the same rows are in `requests.parquet`), the set-aside cells' rows, and the router's
own per-request records (470 MB, 43 MB gzipped), which would double this directory while
largely duplicating what the harness rows already carry.
