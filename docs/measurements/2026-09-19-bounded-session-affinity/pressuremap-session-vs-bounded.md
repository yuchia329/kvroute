# The pressure map — where cache-aware routing pays

Goodput delta between **session_affinity** and **bounded_session_affinity** across the working set × skew grid,
at a single concurrency. Positive means bounded_session_affinity served more requests inside the SLO.

SLO: TTFT < 990ms, inter-token p50 < 24ms. Goodput is requests per second that met it,
so a policy that completed more requests can still score lower.

Each point of the grid is its own comparison: the two policies there sent the same
bytes, which is what makes them comparable, and two different points did not — so
figures are compared down a column and along a row, never against another grid's.

A delta marked *within spread* is smaller than the run-to-run range of the
repetitions behind it, which is a difference between a policy and itself.

> ⚠️ **The WS axis is labelled with what each cell was configured for, not what it
> applied.** Skew discounts working set — concentrating the draws touches fewer
> distinct conversations — and a cell of finite length cannot touch a pool bigger
> than its visit count, which bites hardest at the top of the axis. Counted from
> the session column of #18's 300 s cells rather than modelled: at WS 1 a cell
> realised 0.95 of its label at skew 0 and 0.49 at skew 1.4, and at skew 0 the
> four labels realised 0.25, 0.95, 1.78 and 2.26. A shorter cell realises less.
> So the realised pressure rises more slowly than the labels do, especially to
> the right and at the top. Neither discount is corrected here: correcting either
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

| point | redundant prefill / request | redundant prefill, tokens | hit rate spread | spill: residency | spill: load | spill rate | exercised |
|---|---:|---:|---:|---:|---:|---:|---|
| WS 0.25, skew 0 | 1394.0 | 8742042 | 45.4 pp | 0 | 626 | 0.000% | yes |
| WS 0.25, skew 1 | 647.2 | 4325009 | 21.2 pp | 0 | 243 | 0.000% | yes |
| WS 0.25, skew 1.4 | 295.8 | 4305938 | 9.7 pp | 0 | 87 | 0.000% | yes |
| WS 1, skew 0 | 1354.1 | 6435860 | 43.6 pp | 0 | 159 | 0.000% | yes |
| WS 1, skew 1 | 877.8 | 6426898 | 28.7 pp | 0 | 253 | 0.000% | yes |
| WS 1, skew 1.4 | 431.9 | 3211832 | 14.2 pp | 0 | 98 | 0.000% | yes |
| WS 3, skew 0 | 1201.5 | 5507651 | 38.6 pp | 0 | 31 | 0.000% | yes |
| WS 3, skew 1 | 936.1 | 5929070 | 30.6 pp | 0 | 199 | 0.000% | yes |
| WS 3, skew 1.4 | 440.8 | 4840155 | 14.5 pp | 0 | 118 | 0.000% | yes |
| WS 8, skew 0 | 1173.6 | 5240061 | 37.7 pp | 0 | 22 | 0.000% | yes |
| WS 8, skew 1 | 964.1 | 5668728 | 31.5 pp | 0 | 146 | 0.000% | yes |
| WS 8, skew 1.4 | 471.4 | 4836879 | 15.5 pp | 0 | 104 | 0.000% | yes |

The mechanism fired and at least one point separated by more than its spread.
The map below is measuring the policies.

## The map — Δ goodput, bounded_session_affinity against session_affinity

At 32 users:

| WS \ skew | 0 | 1 | 1.4 |
|---|---:|---:|---:|
| **0.25** | -7.3% (within spread) | +245.8% | +347.7% |
| **1** | +26.4% | +16.7% | +138.4% |
| **3** | +1.4% (within spread) | +27.8% | +295.6% |
| **8** | -0.4% (within spread) | +8.9% (within spread) | +133.2% |

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

| point | load | round_robin | least_outstanding | session_affinity | bounded_session_affinity | prefix_affinity |
|---|---:|---:|---:|---:|---:|---:|
| WS 0.25, skew 0 | 32 users | 5.40 (5.07–5.42, n=3)<br>±6% | 11.23 (10.99–11.38, n=3)<br>±3% | 34.37 (30.11–34.77, n=3)<br>±14% | 31.86 (31.43–32.87, n=3)<br>±5% | 32.04 (32.03–34.02, n=3)<br>±6% |
| WS 0.25, skew 1 | 32 users | 9.87 (9.87–10.06, n=2)<br>±2% | 17.84 (17.84–18.96, n=2)<br>±6% | 8.83 (8.83–24.61, n=2)<br>±179% | 30.55 (30.46–30.63, n=3)<br>±1% | 34.43 (34.20–34.94, n=3)<br>±2% |
| WS 0.25, skew 1.4 | 32 users | 16.27 (16.04–16.57, n=3)<br>±3% | 25.35 (24.33–26.02, n=3)<br>±7% | 7.36 (5.15–13.86, n=3)<br>±118% | 32.93 (32.35–33.75, n=3)<br>±4% | 34.79 (34.67–35.70, n=3)<br>±3% |
| WS 1, skew 0 | 32 users | 3.51 (3.37–3.55, n=3)<br>±5% | 7.84 (7.71–8.16, n=3)<br>±6% | 8.99 (8.19–10.24, n=3)<br>±23% | 11.37 (11.19–11.42, n=3)<br>±2% | 14.94 (14.72–15.10, n=3)<br>±3% |
| WS 1, skew 1 | 32 users | 6.41 (6.33–6.42, n=3)<br>±1% | 13.49 (13.31–13.74, n=3)<br>±3% | 16.96 (12.56–19.07, n=3)<br>±38% | 19.80 (19.15–20.22, n=3)<br>±5% | 23.51 (23.02–23.66, n=3)<br>±3% |
| WS 1, skew 1.4 | 32 users | 11.22 (11.22–11.66, n=2)<br>±4% | 21.01 (20.48–21.27, n=3)<br>±4% | 11.23 (10.89–13.48, n=3)<br>±23% | 26.77 (26.57–28.35, n=3)<br>±7% | 30.45 (30.31–31.42, n=3)<br>±4% |
| WS 3, skew 0 | 32 users | 3.24 (3.16–3.30, n=3)<br>±4% | 7.57 (7.50–7.75, n=3)<br>±3% | 9.32 (8.31–9.58, n=3)<br>±14% | 9.44 (9.43–9.56, n=3)<br>±1% | 11.61 (11.54–11.64, n=3)<br>±1% |
| WS 3, skew 1 | 32 users | 5.15 (5.08–5.28, n=3)<br>±4% | 11.76 (11.71–11.94, n=3)<br>±2% | 12.53 (12.53–14.35, n=2)<br>±15% | 16.01 (15.80–16.54, n=3)<br>±5% | 18.97 (18.85–19.07, n=3)<br>±1% |
| WS 3, skew 1.4 | 32 users | 11.23 (10.86–11.53, n=3)<br>±6% | 19.69 (19.59–19.97, n=3)<br>±2% | 6.44 (5.92–11.84, n=3)<br>±92% | 25.48 (25.20–26.77, n=3)<br>±6% | 28.79 (28.49–29.31, n=3)<br>±3% |
| WS 8, skew 0 | 32 users | 3.21 (3.20–3.24, n=3)<br>±1% | 7.09 (7.02–7.44, n=3)<br>±6% | 8.83 (8.55–9.50, n=3)<br>±11% | 8.80 (8.58–9.04, n=3)<br>±5% | 10.68 (10.46–10.70, n=3)<br>±2% |
| WS 8, skew 1 | 32 users | 4.74 (4.61–4.87, n=3)<br>±5% | 11.05 (10.81–11.20, n=3)<br>±4% | 13.49 (12.82–15.09, n=3)<br>±17% | 14.69 (14.27–14.80, n=3)<br>±4% | 16.91 (16.81–17.05, n=3)<br>±1% |
| WS 8, skew 1.4 | 32 users | 10.21 (10.00–10.22, n=3)<br>±2% | 19.44 (19.03–19.45, n=3)<br>±2% | 10.69 (6.77–11.12, n=3)<br>±41% | 24.92 (24.92–25.37, n=3)<br>±2% | 28.04 (27.73–28.27, n=3)<br>±2% |

## Are the two pressures separable?

The spill rule's two branches are counted apart, and the grid exists because they
answer to different axes: memory pressure evicts and trips the hit-rate low-water mark,
load imbalance piles conversations up and trips the imbalance factor. Each axis is
pooled over the other, so each row isolates one of them.

⚠️ **These cells ran with the residency branch switched off (`bench.Chosen`), so
its column below is zero by construction rather than by measurement.** The load
column is a measurement, and the answer to the question is in the arm named below
rather than in this table.

That the branch was off is a finding rather than an omission. Through #16 it read
`vllm:kv_cache_usage_perc`, which counts blocks held by *running* requests — the
active batch, which the load condition already measures: #16 fitted
kv = 0.02128 + 0.02135 x inflight at r = 0.973 over 13,658 rows, and an idle
replica holding a full cache read 0.021. Arming that mark would have reported a
pass here while measuring one pressure twice.

#28 rebuilt the branch on the engines' own per-replica prefix cache hit rate and
measured it separable from load on fresh rows — r = 0.138 against the gauge's
0.979 (ADR-0011) — and #18 then swept it across these same twelve points with the
load condition off, one mark at a time. **The two branches fire in opposite
directions along the working set axis.** Per 1,000 decisions, up the four working
set levels: residency 16 → 95 → 87 → 88 at its gentlest mark, where load runs
12 → 10 → 8 → 6. Two conditions that were one pressure in two units could not do
that, so the pressures are separable — and the column below is the evidence that
only one of them was armed for these cells.

| working set (skew pooled) | spill: residency | spill: load |
|---|---:|---:|
| 0.25 | 0 | 956 |
| 1 | 0 | 510 |
| 3 | 0 | 348 |
| 8 | 0 | 272 |

| skew (working set pooled) | spill: residency | spill: load |
|---|---:|---:|
| 0 | 0 | 838 |
| 1 | 0 | 841 |
| 1.4 | 0 | 407 |

## What this map does not rest on

Cells excluded from every figure above — §6 discards these rather than averaging
them in:

- `least_outstanding-c32-r3`: the fleet slowed across the measured window: TTFT p50 was 27% slower late than early, compared within each of 4 turn indices, over a 25% threshold. A longer warm-up is not the fix: this cell's latency percentiles are a transient rather than a steady state. Look for a queue that never settled — an open-loop cell offered more than the fleet can serve never reaches one — or a throttled card, a replica lost, or a cache growing
- `round_robin-c32-r3`: still warming up: TTFT p50 was 30% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r2`: still warming up: TTFT p50 was 52% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `round_robin-c32-r2`: still warming up: TTFT p50 was 32% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run
- `session_affinity-c32-r2`: still warming up: TTFT p50 was 32% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run

