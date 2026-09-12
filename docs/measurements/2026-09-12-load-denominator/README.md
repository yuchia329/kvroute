# The spill rule's load denominator — 2026-09-12

#31 found the spill rule declining a fifth of later turns at low open-loop load, against 0.674% at
the 32-user rung its factor was settled at. The condition is written as a ratio — decline the best
prefix match when its replica carries more than a factor times the fleet's minimum inflight — and a
ratio is the property that lets one factor settled at one rung describe the rule at another.

This run measured what the comparison was actually made against at that rung, and then priced every
candidate repair. **Neither repair works, because the rule itself has no operating point there.**

**The comparison was never a ratio.** Across 2,361 weighed decisions the fleet's minimum inflight
was 0 on 96.4% and 1 on the other 3.6% — **never once above 1**. "Twice the fleet minimum" was the
constant 2 for the entire run. The settled rung has this on 16% of its decisions; this rung has it
on 100%.

**Goodput falls monotonically with how often the condition fires, and the best setting is off.**

| point | spills (of requests) | goodput/s | vs off | SLO misses / 601 | TTFT p99 |
|---|---:|---:|---:|---:|---|
| off | 0.0% | **5.99** | — | 0.7 | 662–752 ms |
| 4x min | 1.6% | 5.92 | −1.2% | 8.3 | 756–1347 ms |
| 2x mean | 7.2% | 5.64 | −5.8% | 35.7 | 1560–1639 ms |
| 2x min (`bench.Chosen`) | 11.7% | 5.46 | −8.8% | 54.0 | 1614–2165 ms |

**The ticket's proposed fix helps, and does not rescue it.** Holding the factor against the fleet
mean instead of its minimum recovers a third of the deficit (5.46 → 5.64 of the 0.53/s gap) and it
does so purely by firing less often — 7.2% of decisions against 11.7%. Raising the factor to 4
against the unchanged minimum recovers 87% of it by firing less still. Extrapolate the line to zero
spills and it arrives at the spill-off cell. There is no setting of either axis at this rung that
beats declining nothing.

**Why, and it is not about the driver.** At 32 virtual users the replicas are saturated, so moving a
request off a buried replica buys back more queueing than the prefill costs, and the rule is worth
+80%. At 6 requests per second each replica holds **1.2 requests at the median** — six across the
fleet, against the 32 the settled rung holds — so nothing is queueing, and a declined match pays a
full prefill to escape a wait that was not happening. The rule is not mistuned here — it is
answering a question the fleet is not posing.

## What was held constant

| | |
|---|---|
| policy | `prefix_affinity`, spill point varied per cell — the only thing that varies |
| workload | `multiturn`, the frozen comparison workload: 307 sessions, skew 0, `seed=1` |
| geometry | 4 turns, 448 new prompt tokens a turn, 64 output, 30% shared system prompt, 30% branched |
| load | **open loop, 6 req/s** — `bench.LoadDenominatorRate`, the rung #19 drove policy 4 at |
| cells | 150 s, 50 s warm-up, 10 s settle, **3 repetitions**, fresh fleet per point (ADR-0004) |
| fleet | five cards, GPUs 0 1 2 4 5 — GPU 3 is out (#25) |
| engine | vLLM 0.28.0, `awq_marlin` / `marlin`, block 16, max model length 8192, prefix caching, **KV cache events off** — [`evidence/versions.env`](evidence/versions.env) |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| binaries | `router` md5 `85c1250b0d03`, `bench` `d86cbd623661`, `loadcomparison` `e0b7106e61cf` |
| driver | [`ops/box/run-load-denominator.sh`](../../../ops/box/run-load-denominator.sh) |

Ran 2026-09-12 20:53 → 21:48 UTC, 54 minutes of fleet time. All **10,812** requests across the four
points succeeded: zero dropped, zero failed, zero reroutes, every cell clean and none flagged for
warm-up drift.

## What the comparison was made against

The observing pass is the first cell: prefix affinity with the rule **off**, so the fleet's load is
seen over its natural range rather than over the range the rule's own spilling would produce. Every
row carries both candidate denominators as the decision saw them.

| | |
|---|---|
| fleet minimum inflight | **0 on 2,276 decisions, 1 on 85, never higher** |
| fleet mean inflight | p10 0.60, p50 **1.20**, p90 1.80, max 2.80 |
| mean below the floor of one | **24.2%** of decisions |
| busiest replica weighed | 8 requests |

The second row is why the ticket's repair is only a partial one. `max(minimum, fleet inflight /
replicas)` is the mean — a minimum is never above its own mean, so there is no regime where the
floor releases and the minimum returns — but at this rung the mean is *itself* below the floor of
one on a quarter of decisions, and only reaches 2.80 at its maximum. The denominator it replaces the
constant 1 with is a number between 1 and 2.8.

Full report: [`load-comparison.md`](load-comparison.md).

## The projection, and how well it held

Before any point was run, the observing pass's rows were used to project what each candidate would
have declined. That projection is what cut the grid — 8x against the mean could not fire at all and
4x against the mean would have declined 0.4%, so neither was worth a cell.

Both shares below are of the 2,361 decisions that had a match on the table, which is what the
projection ranges over — not of all 2,703 requests, as the table above is.

| point | projected | actually declined |
|---|---:|---:|
| 2x min | 16.3% | 13.4% |
| 2x mean | 12.7% | 8.3% |

Both over-predict, in the direction the method says they must: a decline that actually happens moves
the request to a quieter replica and relieves the pile-up that would have produced the next one, and
a projection over rows where no declines happened cannot see that feedback. It answers which points
can fire at a rung, which is what a grid is cut from; it does not answer what they are worth, which
is what the cells above are for.

## What this does not say

It does not overturn #18's grid or #16's sweep. Those are closed-loop results at 32 users, where
the fleet minimum is a real quantity and the rule is worth +80%, and nothing here touches them. It
bounds them: `bench.Chosen` describes policy 4 **on a loaded fleet**, and the bound is fleet
utilisation rather than the driver — an open-loop rung high enough to saturate the replicas would
put the minimum back above the floor and the ratio back in the rule.

It also does not settle the denominator for a loaded fleet. The mean is a better-behaved
denominator in principle — it varies where the minimum is pinned — and at this rung that shows up
only as firing less often, which is indistinguishable from a larger factor. Separating the two would
need the pair swept where both denominators are well above the floor, which is the c32 rung, and
this run says nothing about it.
