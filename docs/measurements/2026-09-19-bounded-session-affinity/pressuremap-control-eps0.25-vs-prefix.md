# The pressure map — where cache-aware routing pays

Goodput delta between **bounded_session_affinity** and **prefix_affinity** across the working set × skew grid,
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
| WS 1, skew 0 | 248.2 | 2413584 | 8.1 pp | 0 | 207 | 1.723% | yes |
| WS 1, skew 1.4 | 60.5 | 1270320 | 2.0 pp | 0 | 112 | 0.477% | yes |

The mechanism fired and at least one point separated by more than its spread.
The map below is measuring the policies.

## The map — Δ goodput, prefix_affinity against bounded_session_affinity

At 32 users:

| WS \ skew | 0 | 1.4 |
|---|---:|---:|
| **1** | +31.7% | +11.6% |

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

| point | load | session_affinity | bounded_session_affinity | prefix_affinity |
|---|---:|---:|---:|---:|
| WS 1, skew 0 | 32 users | 9.40 (8.48–10.62, n=3)<br>±23% | 11.53 (11.13–11.68, n=3)<br>±5% | 15.19 (14.97–15.44, n=3)<br>±3% |
| WS 1, skew 1.4 | 32 users | 10.36 (10.36–13.77, n=2)<br>±33% | 27.65 (27.42–28.07, n=3)<br>±2% | 30.86 (30.57–32.01, n=3)<br>±5% |

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
| 1 | 0 | 319 |

| skew (working set pooled) | spill: residency | spill: load |
|---|---:|---:|
| 0 | 0 | 207 |
| 1.4 | 0 | 112 |

## What this map does not rest on

Points of the grid with no cells at all — the run is this much short of complete:

- WS 0.25, skew 0
- WS 0.25, skew 1
- WS 0.25, skew 1.4
- WS 1, skew 1
- WS 3, skew 0
- WS 3, skew 1
- WS 3, skew 1.4
- WS 8, skew 0
- WS 8, skew 1
- WS 8, skew 1.4

Cells excluded from every figure above — §6 discards these rather than averaging
them in:

- `session_affinity-c32-r3`: still warming up: TTFT p50 was 32% slower early in the measured window than late, compared within each of 4 turn indices, over a 25% threshold. Lengthen the warm-up and re-run

