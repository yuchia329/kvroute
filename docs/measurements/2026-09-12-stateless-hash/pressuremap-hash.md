# The pressure map — where cache-aware routing pays

Goodput delta between **prefix_hash** and **prefix_affinity** across the working set × skew grid,
at a single concurrency. Positive means prefix_affinity served more requests inside the SLO.

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it,
so a policy that completed more requests can still score lower.

Each point of the grid is its own comparison: the two policies there sent the same
bytes, which is what makes them comparable, and two different points did not — so
figures are compared down a column and along a row, never against another grid's.

A delta marked *within spread* is smaller than the run-to-run range of the
repetitions behind it, which is a difference between a policy and itself.

> ⚠️ **The WS axis is labelled with what each cell was configured for, not what it
> applied.** Skew discounts working set — concentrating the draws touches fewer
> distinct conversations, so at WS 1 a cell realises about 0.97 of its label at
> skew 0 and 0.40 at skew 1.4 — and a cell of finite length cannot touch a pool
> bigger than its visit count, which bites hardest at the top of the axis. So
> the realised pressure rises more slowly than the labels do, especially to the
> right and at the top. Neither discount is corrected here: correcting either
> would change the workload's name and refuse every cell recorded under the old
> one (ADR-0004, ADR-0007). The realised draw is countable from the session
> column of the rows, and a flat top end should be checked against it before it
> is read as a result.

## Did the run apply the pressure it claims to?

Spill only fires under pressure, so a grid run where neither threshold is ever
tripped collapses policy 4 into policy 3 and returns a flat map that says nothing
about either. These three columns are read before the map below it.

Redundant prefill is the worst policy's excess prompt tokens over the best at that
point, **per request served**; hit rate spread is the gap between the highest and
lowest prefix cache hit rate there. The two spill columns stay apart because they
answer to different axes of this grid. An em dash is a counter nobody read, which is
not a zero.

The per-request normalisation is not cosmetic. This grid runs the closed-loop driver,
where a virtual user sends its next turn when its last one returns — so a policy that
answers faster gets further through the same sequence and offers more prompts in the
same window. An identical workload guarantees both policies the same generator, not
the same number of prompts, so a column of absolute totals rises with throughput and
names the slower policy as the one that wasted less. The token column beside it is
the same worst policy's per-request excess against its own requests, and is there so
the size of the waste is visible — it is not what the verdict is read from.

Spill rate is the share of the challenger's decisions that declined a prefix match,
and it is expected to be small. The valve is self-limiting: declining a match moves
load off the replica that was over the threshold, so the condition that fired stops
being true. #16 measured 0.674% of decisions declined on a fleet where an
uncorrected run would have qualified 55% of the time. A near-zero rate here is the
mechanism working, not a rule that failed to fire.

| point | redundant prefill / request | redundant prefill, tokens | hit rate spread | spill: KV | spill: load | spill rate | exercised |
|---|---:|---:|---:|---:|---:|---:|---|
| WS 0.25, skew 0 | 101.6 | 2355266 | 3.3 pp | 0 | 1561 | 2.379% | yes |
| WS 0.25, skew 1 | 43.6 | 1067237 | 1.4 pp | 0 | 852 | 0.865% | yes |
| WS 0.25, skew 1.4 | 23.5 | 587537 | 0.8 pp | 0 | 638 | 0.236% | yes |
| WS 1, skew 0 | 172.8 | 1784968 | 5.6 pp | 0 | 495 | 1.379% | yes |
| WS 1, skew 1 | 92.0 | 1501575 | 3.0 pp | 0 | 851 | 1.460% | yes |
| WS 1, skew 1.4 | 34.2 | 733881 | 1.1 pp | 0 | 518 | 0.481% | yes |
| WS 3, skew 0 | 139.5 | 1223690 | 4.5 pp | 0 | 158 | 0.393% | yes |
| WS 3, skew 1 | 98.2 | 1321922 | 3.2 pp | 0 | 639 | 1.296% | yes |
| WS 3, skew 1.4 | 31.0 | 632204 | 1.0 pp | 0 | 528 | 0.521% | yes |
| WS 8, skew 0 | 126.2 | 1053386 | 4.1 pp | 0 | 147 | 0.198% | yes |
| WS 8, skew 1 | 105.6 | 1283894 | 3.5 pp | 0 | 542 | 0.945% | yes |
| WS 8, skew 1.4 | 46.5 | 901487 | 1.5 pp | 0 | 595 | 0.495% | yes |

The mechanism fired and at least one point separated by more than its spread.
The map below is measuring the policies.

## The map — Δ goodput, prefix_affinity against prefix_hash

At 32 users:

| WS \ skew | 0 | 1 | 1.4 |
|---|---:|---:|---:|
| **0.25** | -6.4% (within spread) | +5.4% | +7.6% |
| **1** | +25.2% | +9.2% | +7.7% |
| **3** | +20.1% | +12.4% | +8.7% |
| **8** | +17.9% | +17.2% | +11.0% |

## How much of the gain does the approximate index keep?

The share of the gain exact knowledge of the caches buys over session affinity that prefix
affinity keeps without it: (prefix affinity − session affinity) / (exact residency − session
affinity), in goodput medians. 100% is all of it; above 100% the approximation beat the
exact policy; below 0% it did worse than the baseline both exist to beat. No share is claimed
where exact residency's own gain is inside the run-to-run spread: a share of noise is noise.

The TTFT columns are there for the comparison this replicates. llm-d published its
precise-versus-approximate result as P90 TTFT — 0.54 s precise against 31.1 s approximate —
at datacentre scale, and the last row carries it. It is set beside these figures, not ranked
against them: a different fleet, model and workload, where this is one host of consumer cards
whose caches are far smaller and evict far more often (ADR-0010).

| point | load | share of the gain kept | session affinity | prefix affinity | exact residency | TTFT p90, prefix affinity | TTFT p90, exact residency |
|---|---:|---:|---:|---:|---:|---:|---:|
| WS 0.25, skew 0 | 32 users | — | 34.53 (29.13–35.48, n=3) | 32.15 (31.95–34.53, n=3) | 30.00 (29.27–30.95, n=3) | 236ms | 429ms |
| WS 0.25, skew 1 | 32 users | 113% | 8.71 (8.71–24.78, n=2) | 34.66 (34.62–36.03, n=3) | 31.69 (30.70–34.04, n=3) | 120ms | 395ms |
| WS 0.25, skew 1.4 | 32 users | 111% | 7.07 (5.17–13.87, n=3) | 35.16 (34.91–36.28, n=3) | 32.46 (32.27–33.99, n=3) | 120ms | 362ms |
| WS 1, skew 0 | 32 users | 130% | 9.15 (8.57–9.97, n=3) | 15.17 (15.04–15.22, n=3) | 13.78 (13.19–14.25, n=3) | 799ms | 990ms |
| WS 1, skew 1 | 32 users | 152% | 17.31 (12.43–18.37, n=3) | 23.18 (23.00–23.99, n=3) | 21.16 (21.14–21.42, n=3) | 470ms | 734ms |
| WS 1, skew 1.4 | 32 users | 113% | 10.43 (10.43–13.55, n=2) | 30.63 (30.06–31.84, n=3) | 28.34 (28.02–29.96, n=3) | 395ms | 464ms |
| WS 3, skew 0 | 32 users | 181% | 8.97 (8.13–9.67, n=3) | 11.64 (11.50–11.75, n=3) | 10.45 (10.16–10.64, n=3) | 972ms | 1141ms |
| WS 3, skew 1 | 32 users | 130% | 12.00 (12.00–13.50, n=2) | 19.02 (19.00–19.43, n=3) | 17.39 (17.28–17.76, n=3) | 727ms | 846ms |
| WS 3, skew 1.4 | 32 users | 106% | 6.55 (5.91–11.38, n=3) | 28.95 (28.78–29.16, n=3) | 27.60 (26.64–27.78, n=3) | 421ms | 473ms |
| WS 8, skew 0 | 32 users | — | 8.71 (8.06–9.03, n=3) | 10.76 (10.65–10.99, n=3) | 9.30 (9.01–9.72, n=3) | 999ms | 1339ms |
| WS 8, skew 1 | 32 users | 278% | 13.76 (12.97–14.43, n=3) | 17.42 (16.83–17.47, n=3) | 15.08 (14.74–15.39, n=3) | 732ms | 1013ms |
| WS 8, skew 1.4 | 32 users | 111% | 10.37 (6.72–11.72, n=3) | 28.23 (27.82–28.35, n=3) | 26.46 (25.62–26.58, n=3) | 420ms | 488ms |
| llm-d, published (datacentre scale) | — | — | — | — | — | 31.1 s | 0.54 s |

Where no share is claimed, and why:

- WS 0.25, skew 0 at 32 users: exact residency's gain over session affinity is inside the spread, so there is no gain to share
- WS 8, skew 0 at 32 users: exact residency's gain over session affinity is inside the spread, so there is no gain to share

## Goodput behind the map

Each figure is the median of that point's repetitions with the range across them,
and the spread beneath it as a share of that median. An em dash is a policy with no
usable cell at that point, which is not a zero.

⚠️ Read a wide spread as two states rather than as noise. #16 measured the spill-off
reference at this load rung and skew 1.4 returning 10.76, 17.05 and 22.01, with SLO
violation rates tracking each one exactly — which is bistability, not variance. The
spill valve damps it for the challenger; the policies without one have nothing to
damp it, so expect it in their high-skew cells. A median over a bimodal sample is
whichever state won most draws, and more repetitions will not make it unimodal.

| point | load | session_affinity | prefix_hash | prefix_affinity | exact_residency |
|---|---:|---:|---:|---:|---:|
| WS 0.25, skew 0 | 32 users | 34.53 (29.13–35.48, n=3)<br>±18% | 34.34 (33.75–34.86, n=3)<br>±3% | 32.15 (31.95–34.53, n=3)<br>±8% | 30.00 (29.27–30.95, n=3)<br>±6% |
| WS 0.25, skew 1 | 32 users | 8.71 (8.71–24.78, n=2)<br>±184% | 32.87 (31.99–34.08, n=3)<br>±6% | 34.66 (34.62–36.03, n=3)<br>±4% | 31.69 (30.70–34.04, n=3)<br>±11% |
| WS 0.25, skew 1.4 | 32 users | 7.07 (5.17–13.87, n=3)<br>±123% | 32.69 (32.65–33.41, n=3)<br>±2% | 35.16 (34.91–36.28, n=3)<br>±4% | 32.46 (32.27–33.99, n=3)<br>±5% |
| WS 1, skew 0 | 32 users | 9.15 (8.57–9.97, n=3)<br>±15% | 12.11 (12.04–12.64, n=3)<br>±5% | 15.17 (15.04–15.22, n=3)<br>±1% | 13.78 (13.19–14.25, n=3)<br>±8% |
| WS 1, skew 1 | 32 users | 17.31 (12.43–18.37, n=3)<br>±34% | 21.22 (20.76–21.22, n=3)<br>±2% | 23.18 (23.00–23.99, n=3)<br>±4% | 21.16 (21.14–21.42, n=3)<br>±1% |
| WS 1, skew 1.4 | 32 users | 10.43 (10.43–13.55, n=2)<br>±30% | 28.45 (27.38–29.03, n=3)<br>±6% | 30.63 (30.06–31.84, n=3)<br>±6% | 28.34 (28.02–29.96, n=3)<br>±7% |
| WS 3, skew 0 | 32 users | 8.97 (8.13–9.67, n=3)<br>±17% | 9.70 (9.50–9.97, n=3)<br>±5% | 11.64 (11.50–11.75, n=3)<br>±2% | 10.45 (10.16–10.64, n=3)<br>±5% |
| WS 3, skew 1 | 32 users | 12.00 (12.00–13.50, n=2)<br>±13% | 16.93 (16.87–16.93, n=3)<br>±0% | 19.02 (19.00–19.43, n=3)<br>±2% | 17.39 (17.28–17.76, n=3)<br>±3% |
| WS 3, skew 1.4 | 32 users | 6.55 (5.91–11.38, n=3)<br>±83% | 26.64 (26.37–27.21, n=3)<br>±3% | 28.95 (28.78–29.16, n=3)<br>±1% | 27.60 (26.64–27.78, n=3)<br>±4% |
| WS 8, skew 0 | 32 users | 8.71 (8.06–9.03, n=3)<br>±11% | 9.12 (9.07–9.31, n=3)<br>±3% | 10.76 (10.65–10.99, n=3)<br>±3% | 9.30 (9.01–9.72, n=3)<br>±8% |
| WS 8, skew 1 | 32 users | 13.76 (12.97–14.43, n=3)<br>±11% | 14.86 (14.76–15.32, n=3)<br>±4% | 17.42 (16.83–17.47, n=3)<br>±4% | 15.08 (14.74–15.39, n=3)<br>±4% |
| WS 8, skew 1.4 | 32 users | 10.37 (6.72–11.72, n=3)<br>±48% | 25.42 (25.22–25.65, n=3)<br>±2% | 28.23 (27.82–28.35, n=3)<br>±2% | 26.46 (25.62–26.58, n=3)<br>±4% |

## Are the two pressures separable?

The spill rule's two branches are counted apart, and the grid exists because they
answer to different axes: memory pressure evicts and trips the KV high-water mark,
load imbalance piles conversations up and trips the imbalance factor. Each axis is
pooled over the other, so each row isolates one of them.

🚧 **This grid cannot answer that question, and the tables below must not be read
as though it had.** The KV high-water branch was switched off for these cells
(`bench.Chosen`), so its column is zero everywhere by construction rather than by
measurement.

The reason is a property of the metric, not of this grid. `vllm:kv_cache_usage_perc`
counts blocks held by *running* requests, so it reads the active batch and not cache
residency: #16 measured kv = 0.02128 + 0.02135 x inflight at r = 0.973 over 13,658
rows, and an idle replica holding a full cache reads 0.021. On this fleet the KV
branch *is* the load branch, so any non-zero mark either cannot fire or fires on
load — which the imbalance factor already covers. Arming it would make this
section report a pass while measuring one pressure twice.

So separability is **blocked on #28**, which needs a residency metric the engine
does not currently expose. This is a real scoping loss for the grid rather than a
presentational one: the other six acceptance criteria stand, and this one waits.
The tables are still printed, because the load column remains a measurement and
the KV column is the evidence that it was never armed.

| working set (skew pooled) | spill: KV | spill: load |
|---|---:|---:|
| 0.25 | 0 | 3051 |
| 1 | 0 | 1864 |
| 3 | 0 | 1325 |
| 8 | 0 | 1284 |

| skew (working set pooled) | spill: KV | spill: load |
|---|---:|---:|
| 0 | 0 | 2361 |
| 1 | 0 | 2884 |
| 1.4 | 0 | 2279 |

## What this map does not rest on

Cells excluded from every figure above — §6 discards these rather than averaging
them in:

- `session_affinity-c32-r2`: still warming up: the first half of the measured window was 39% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r3`: still warming up: the first half of the measured window was 27% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r3`: still warming up: the first half of the measured window was 62% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run

