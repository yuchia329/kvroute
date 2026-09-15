# The pressure map — where cache-aware routing pays

> ⚠️ **Historical artifact, kept as it was drawn.** Its validity table's redundant-prefill column
> is the absolute recomputed-token spread, which under this closed loop credits the slower policy
> ([#30](https://github.com/yuchia329/kvroute/issues/30)). It is not regenerated because it was
> drawn over session and prefix affinity alone, before the two context policies ran, and that
> subset no longer exists as a directory. Read the per-request column in
> [`pressuremap.md`](pressuremap.md), and the re-read table in [`README.md`](README.md).


Goodput delta between **session_affinity** and **prefix_affinity** across the working set × skew grid,
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
point; hit rate spread is the gap between the highest and lowest prefix cache hit
rate there. The two spill columns stay apart because they answer to different axes
of this grid. An em dash is a counter nobody read, which is not a zero.

Spill rate is the share of the challenger's decisions that declined a prefix match,
and it is expected to be small. The valve is self-limiting: declining a match moves
load off the replica that was over the threshold, so the condition that fired stops
being true. #16 measured 0.674% of decisions declined on a fleet where an
uncorrected run would have qualified 55% of the time. A near-zero rate here is the
mechanism working, not a rule that failed to fire.

| point | redundant prefill | hit rate spread | spill: KV | spill: load | spill rate | exercised |
|---|---:|---:|---:|---:|---:|---|
| WS 0.25, skew 0 | 1696807 | 2.2 pp | 0 | 626 | 2.512% | yes |
| WS 0.25, skew 1 | 940082 | 0.5 pp | 0 | 243 | 0.932% | yes |
| WS 0.25, skew 1.4 | 652483 | 0.1 pp | 0 | 87 | 0.329% | yes |
| WS 1, skew 0 | 1918013 | 0.9 pp | 0 | 159 | 1.339% | yes |
| WS 1, skew 1 | 1488008 | 1.1 pp | 0 | 253 | 1.416% | yes |
| WS 1, skew 1.4 | 1597099 | 0.1 pp | 0 | 98 | 0.423% | yes |
| WS 3, skew 0 | 1170496 | 1.0 pp | 0 | 31 | 0.321% | yes |
| WS 3, skew 1 | 1487117 | 0.5 pp | 0 | 199 | 1.347% | yes |
| WS 3, skew 1.4 | 1375506 | 0.4 pp | 0 | 118 | 0.541% | yes |
| WS 8, skew 0 | 836329 | 1.3 pp | 0 | 22 | 0.243% | yes |
| WS 8, skew 1 | 1152016 | 0.3 pp | 0 | 146 | 1.091% | yes |
| WS 8, skew 1.4 | 1401321 | 0.3 pp | 0 | 104 | 0.491% | yes |

The mechanism fired and at least one point separated by more than its spread.
The map below is measuring the policies.

## The map — Δ goodput, prefix_affinity against session_affinity

At 32 users:

| WS \ skew | 0 | 1 | 1.4 |
|---|---:|---:|---:|
| **0.25** | -6.8% (within spread) | +289.8% | +373.0% |
| **1** | +66.2% | +38.6% | +179.5% |
| **3** | +24.6% | +41.3% | +346.9% |
| **8** | +21.0% | +25.3% | +162.4% |

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

| point | load | session_affinity | prefix_affinity |
|---|---:|---:|---:|
| WS 0.25, skew 0 | 32 users | 34.37 (30.11–34.77, n=3)<br>±14% | 32.04 (32.03–34.02, n=3)<br>±6% |
| WS 0.25, skew 1 | 32 users | 8.83 (8.83–24.61, n=2)<br>±179% | 34.43 (34.20–34.94, n=3)<br>±2% |
| WS 0.25, skew 1.4 | 32 users | 7.36 (5.15–13.86, n=3)<br>±118% | 34.79 (34.67–35.70, n=3)<br>±3% |
| WS 1, skew 0 | 32 users | 8.99 (8.19–10.24, n=3)<br>±23% | 14.94 (14.72–15.10, n=3)<br>±3% |
| WS 1, skew 1 | 32 users | 16.96 (12.56–19.07, n=3)<br>±38% | 23.51 (23.02–23.66, n=3)<br>±3% |
| WS 1, skew 1.4 | 32 users | 10.89 (10.89–13.48, n=2)<br>±24% | 30.45 (30.31–31.42, n=3)<br>±4% |
| WS 3, skew 0 | 32 users | 9.32 (8.31–9.58, n=3)<br>±14% | 11.61 (11.54–11.64, n=3)<br>±1% |
| WS 3, skew 1 | 32 users | 13.42 (12.53–14.35, n=3)<br>±14% | 18.97 (18.85–19.07, n=3)<br>±1% |
| WS 3, skew 1.4 | 32 users | 6.44 (5.92–11.84, n=3)<br>±92% | 28.79 (28.49–29.31, n=3)<br>±3% |
| WS 8, skew 0 | 32 users | 8.83 (8.55–9.50, n=3)<br>±11% | 10.68 (10.46–10.70, n=3)<br>±2% |
| WS 8, skew 1 | 32 users | 13.49 (12.82–15.09, n=3)<br>±17% | 16.91 (16.81–17.05, n=3)<br>±1% |
| WS 8, skew 1.4 | 32 users | 10.69 (6.77–11.12, n=3)<br>±41% | 28.04 (27.73–28.27, n=3)<br>±2% |

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
| 0.25 | 0 | 956 |
| 1 | 0 | 510 |
| 3 | 0 | 348 |
| 8 | 0 | 272 |

| skew (working set pooled) | spill: KV | spill: load |
|---|---:|---:|
| 0 | 0 | 838 |
| 1 | 0 | 841 |
| 1.4 | 0 | 407 |

## What this map does not rest on

Cells excluded from every figure above — §6 discards these rather than averaging
them in:

- `session_affinity-c32-r2`: still warming up: the first half of the measured window was 29% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r3`: still warming up: the first half of the measured window was 27% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run

