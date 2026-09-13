# ADR-0011: The KV gauge measures the batch, so the spill rule reads cache residency

**Status:** Accepted · **Date:** 2026-09-12 · **Amended:** 2026-09-12 (decision 1, on measurement) · **Amended:** 2026-09-13 (decision 6, on the sweep)

## Context

The spill rule (#16, idea.md §4.1) declines a prefix match under two conditions, and its whole
justification is that the two answer different pressures. `vllm:kv_cache_usage_perc` above a
high-water mark was memory pressure — the replica is out of cache room, so the match believed in
there is already being evicted. Inflight above a multiple of the fleet minimum was load imbalance
— the replica is buried, so the match would be paid for in queueing rather than in prefill. The
pressure grid (#18) crosses working set against skew precisely because those two knobs were
believed to fire the two branches separately, and its headline claim is that *"memory pressure and
load imbalance are separable in the results, since they drive different branches of the spill
rule."*

The KV branch was not measuring memory pressure. It was measuring the active batch, which is what
the load branch already measures.

**The evidence.** 13,658 router rows from #16's KV-axis run (WS 3, skew 0, c32, spill-off through
0.95):

```
kv_cache_usage_perc   p50 = 0.088   p90 = 0.513   p99 = 0.665   max = 0.753
                      >0.70: 0.38%   >0.85: 0.000%   >0.95: 0.000%

kv = 0.02128 + 0.02135 x inflight            Pearson r = 0.973
```

| inflight | 0 | 4 | 8 | 16 | 24 | 27 |
|---|---:|---:|---:|---:|---:|---:|
| mean kv | 0.021 | 0.109 | 0.182 | 0.360 | 0.530 | 0.595 |

Three consequences, in order of how badly each hurts.

**The gauge is blind to cache residency.** The intercept says an idle replica reads 0.021. That
run offered WS 3 — three times the session tokens the fleet can hold — and `derive-ttl` measured a
57-second idle-before-evict tail off 5,076 real evictions on this same fleet. The engines were
evicting continuously while the gauge read 8.8% at the median, because the allocator counts blocks
held by *running* requests. Blocks holding the cached prefixes of finished requests are free from
its point of view — reclaimable, waiting to be reused or evicted — and they are exactly the blocks
a prefix match depends on.

**Two of the three grid levels were arithmetically unreachable.** Inverting the fit: 0.70 needs
31.8 concurrent requests on one replica, 0.85 needs 38.8, 0.95 needs 43.5. There were 32 virtual
users in the whole run and the largest inflight ever observed on a replica was 31. Those levels
did not fail to fire; they could not. 0.70 sat at the ceiling and caught 0.38% of moments, giving
0.14% of decisions.

**The two conditions were one condition.** On a fleet whose minimum inflight sits at 1, factor 32
≈ KV 0.70, factor 39 ≈ KV 0.85, factor 44 ≈ KV 0.95. The grid was sweeping one pressure twice.

Raising concurrency does not repair it. At c128 a replica could hold 128 requests and the gauge
would saturate, but at r = 0.973 the KV branch would then be firing on queue depth, and the grid
would report memory pressure and load imbalance as separable while both columns were driven by one
signal — a result that looks like a pass and is worth less than a failure.

## Decision

**1. The residency branch reads the engines' own per-replica prefix cache hit rate, over a moving
window.** This decision was taken twice. What follows records the first choice, why it was made,
and the measurement that overturned it, because the reasoning was sound and the result was not.

*Superseded, 2026-09-12: honoured belief, fed back from the engines' own answers.* Since ADR-0008 every response carries `usage.prompt_tokens_details.cached_tokens`: the
engine's own account of how much of this prompt it really had. The router already knows what it
claimed that replica was holding when it routed there. Held against each other per request and
averaged over a bounded window, they are a direct measurement of whether a replica is still
honouring the index's beliefs — and a replica that has stopped honouring beliefs is a replica that
is evicting them.

This is the truest signal available and it costs no scrape: it rides responses the router already
proxies. It also closes the loop between the belief divergence #17 measures after a run and the
rule #16 applies during one — the same quantity, per replica and live.

The alternatives looked weaker. Per-replica prefix cache hit rate (`vllm:prefix_cache_hits_total`
over `_queries_total`) is cheapest and scrapes on the existing tick, but it is lagging and
aggregate: it reports that eviction already happened rather than that this replica is evicting
what this router believes in. An index-side estimate — the router counting the distinct blocks it
has routed to each replica against a per-replica share of fleet capacity — needs no engine
cooperation at all, but it is the model checking itself rather than being checked, which is the
failure ADR-0006 exists to refuse.

**What the measurement said.** The observing pass ran on 2026-09-12 (WS 3, skew 0, c32, spill off,
4,519 decisions) and the honoured rate came back saturated:

```
honoured rate     min 0.942  p10 0.985  p50 1.000  p90 1.000  max 1.000
                  70.1% of readings exactly 1.0;  11.1% below 0.99;  3.2% below 0.97
                  r = -0.702 against inflight
batch KV gauge    min 0.000  p10 0.019  p50 0.078  p90 0.539  max 0.757
                  r = 0.977 against inflight
```

The gauge's r = 0.977 against #16's 0.973 replicates this ADR's diagnosis on fresh cells, which is
the half that held. The honoured rate did not: bucketed by load its mean runs from 0.9997 at
inflight 0–1 to 0.9824 at 20–21, so it moves 1.7 percentage points across the whole range. Its
r = −0.702 is a strong coefficient over a signal with almost no range — the relationship is
monotone, not large — and reading "materially below 0.973" off it and stopping would be spin.
No grid can be cut from that, which is acceptance criterion 4.

Replaying the run's own `(claimed, held)` pairs through five windows, from 64 requests/30 s down to
8/5 s, settled why. The spread widened 2.4× (min 0.942 → 0.862) while the predictive gap — how much
less the *next* request on a replica is honoured after a low reading than after a high one — stayed
flat at ~1.6 points and got slightly worse at the shortest windows. Shortening the window
manufactures range without manufacturing information, so the saturation is structural rather than an
artefact of smoothing.

**The cause is this project's own calibration.** ADR-0006 and ADR-0008 size the prefix index so it
does not over-predict: its TTL and node cap make it drop beliefs *before* the engines evict the
blocks. It is therefore almost always right about whatever it still claims, and "are this replica's
claims honoured" measures the index's conservatism rather than the replica's memory pressure. No
working set fixes that, because the index grows more conservative exactly as the fleet grows
tighter.

**So the branch reads the hit rate instead**, which the same run measured at 68.7% fleet-wide
(1,943,104 of 2,829,217 block queries) — it has the range honoured belief structurally lacks. Its
lag, the reason it was passed over first, is real and is the price: it reports eviction that has
happened rather than eviction about to. A lagging signal with range beats a prompt one pinned at
1.0.

The honoured rate is kept as a recorded column, not routed on — the same treatment the batch gauge
got, and for the same reason. It is the live counterpart of the belief divergence #17 measures after
a run, it costs one ring step per response, and a column that shows the saturation in every later
run is cheaper than a run that rediscovers it.

**2. The mark is a low-water mark, and the grid is cut from an observed range or not at all.** The
two signals run in opposite directions: a replica in trouble reads high on an occupancy gauge and
low on a rate of blocks its cache answered. `bench.HitRateLowWaterGrid` was therefore deliberately
empty, and `HitRateLowWaterSweep` returned the spill-off reference alone, until a run had been
taken to cut it against. Every router row records the rate whether or not a threshold consults it,
so the pass at WS 3, skew 0 with the condition off *is* the observing run; `cmd/spillsignal`
reports the observed range and `Range.Reaches` refuses a level the signal never got below. #16 cut
three levels against a signal nobody had seen the range of, and that is the mistake being unwound
here — repeating it in a new unit would be no better.

That run was taken on 2026-09-12 and the grid is **0.55 / 0.62 / 0.70**. See *What the hit rate
measured* below for the range and for what the levels were chosen against, which is not only
reachability: #16's 0.70 was reachable too, and fired on 0.14% of decisions.

**3. Failure is silence at every step, never a zero.** A scrape that did not answer, an engine
version that renamed the counters, a window holding fewer than its floor of block queries, a
replica whose counters went backwards because it restarted inside the window: each leaves the rate
unread rather than low, and an unread rate is never under any mark. The same rule covers the
honoured column's own failures — a response with no usage block, a replica started without
`--enable-prompt-tokens-details`, a policy that claimed nothing for this request, a prompt the row
cannot convert to tokens. A fleet with no signal therefore routes exactly as it did before the
signal existed.

This matters more for a residency mark than for the gauge it replaced, because the degradation is
asymmetric in the dangerous direction. The old branch spilled when a gauge read *high*, so an
unread reading of zero was harmless. This one spills when a rate reads *low*, so a zero would read
as *"this replica is evicting everything"* — and a fleet reported that way on a failed scrape or a
missing engine flag would spill itself apart at the moment it lost its instrumentation. Hence the
floor of 100 queries and `HitRate.Under` refusing an unread reading in the type rather than at the
call site: 1.7% of this run's rows were unread, spread throughout it rather than bunched at the
start, and every one of them kept its match.

**4. `vllm:kv_cache_usage_perc` is kept, renamed, and routed on by nothing.** It is now
`vllmmetrics.BatchOccupancy`, read from the same single GET as the prefix-cache counters whenever
the router scrapes at all, and recorded as `batch_kv_occupancy` beside the `inflight` of the same
decision. The name says what it measures.
It is kept rather than dropped for one reason: it is the control. The claim above is that one
signal tracks inflight and the other does not, and that claim stays checkable from any run's own
rows only if both are on them.

**5. Separability becomes a measurement rather than a premise.** `bench.MeasureSpillSignals`
reports each signal's range and its Pearson correlation with the inflight recorded on the same
row, and `cmd/spillsignal` prints them side by side. #18's criterion cannot be evaluated until a
run has shown the residency signal's correlation with inflight to be materially below 0.973.

**6. The branch is built, and it stays disabled: `bench.Chosen` keeps `HitRateLowWater: 0`.** The
consequences below left this open — *"whether it costs anything is a question for the sweep: the
decision mix and the goodput at each mark are what price it"*. #18's residency arm is that sweep,
run on 2026-09-13: prefix affinity with the mark alone and the load condition off at every cell, at
all three levels, across the pressure grid's twelve points with three repetitions, the same
geometry and the same bytes as the grid and its spill-off arm. Full record:
[`docs/measurements/2026-09-11-pressure-grid`](../measurements/2026-09-11-pressure-grid/), section
*Are the two pressures separable?*

**Every mark loses to the load condition at every point it has a usable cell for — 35 of 35, by
5% to 91%.** Goodput down the skew-0 column, where the branch fires hardest:

| WS | no spill rule | load only (0/2) | 0.55 | 0.62 | 0.70 |
|---|---:|---:|---:|---:|---:|
| 0.25 | 35.16 | 32.04 | 24.73 | 27.99 | 3.01 |
| 1 | 8.13 | **14.94** | 2.28 | 2.75 | 4.44 |
| 3 | 5.97 | **11.61** | 2.60 | 3.37 | 5.94 |
| 8 | 6.51 | **10.68** | 2.81 | 4.11 | 6.84 |

Three findings, in the order they bear on the decision.

**The rule is self-defeating on this signal.** Declining a match because a replica is evicting
sends the request to a replica that never held the conversation, which is a certain miss, so the
fleet's hit rate falls, which puts more decisions under the mark. At WS 1 / skew 0 the fleet reads
77.5% under the load condition and 45.5% at mark 0.55, where 44.1% of decisions spill — against
the 5.2% *What the hit rate measured* predicted for that level, because the observing pass
measured the signal on a run that was not routing on it. A threshold read off an unarmed run
understates its own firing rate once armed, and that is a general caution for this project, not a
fact about 0.55.

**The branch acts least where memory pressure is worst.** `Spill.targets` excludes every replica
also under the mark, so a fleet evicting everywhere leaves an empty target set and the match is
kept by design — the property that stops a spill relieving nothing. Its consequence is that the
rule's firing is highest where the fleet *straddles* the mark and lowest where the fleet is
entirely beneath it. Tightening 0.55 → 0.70 at skew 0 above WS 0.25 cuts firing from 44.1 / 39.7 /
35.8% of decisions to 24.2 / 14.0 / 8.4% and recovers goodput toward a third of the load
condition's. A stricter mark is not a stronger rule here; it is a rule that has stopped firing.
The design fault is in what a declined request is allowed to be sent to, not in where the
threshold sits, so no level of this grid could have been the answer.

**The signal is not what failed.** Decision 1 stands. Across the twelve points the residency
branch's firing rises three- to tenfold up the working set axis — 16 → 95 → 87 → 88 per 1,000
decisions at 0.55, 15 → 115 → 131 → 154 at 0.62, 65 → 189 → 141 → 140 at 0.70 — while the load
condition's falls by half over the same axis, 12 → 10 → 8 → 6. Two conditions reading one pressure
in two units, which is what these were before this ADR, cannot run in opposite directions on the
axis that drives eviction. **That is #18's separability criterion, met.** A signal that answers to
the right pressure and a rule that acts harmfully on it are different things, and only the rule is
disabled.

## What the hit rate measured

The observing pass was re-run against the new signal on 2026-09-12 (WS 3, skew 0, c32, spill off,
4,022 decisions, all three signals on every row from one GET per replica per tick). Full record:
[`docs/measurements/2026-09-12-spill-signal`](../measurements/2026-09-12-spill-signal/).

```
prefix cache hit rate   3955 read, 67 unread
                        min 0.014  p10 0.560  p50 0.662  p90 0.735  max 0.908
                        r = 0.138 against inflight
batch KV gauge          4022 read, 0 unread
                        min 0.000  p10 0.009  p50 0.066  p90 0.583  max 0.848
                        r = 0.979 against inflight
```

The gauge replicating at r = 0.979, against 0.977 on the honoured-rate pass and 0.973 on #16's
rows, is this ADR's diagnosis holding across three separate runs.

**The coefficient is the weaker half of the claim, and the effect size is the honest one.** This
project has already been misled once by an r taken over a signal with no range — the honoured
rate's r = −0.702 across 1.7 percentage points — so the same question is asked here in the units
the rule uses. Bucketed by load, the hit rate's mean runs 0.659 at inflight ≤ 1 to 0.654 at
inflight ≥ 6: **0.6 points across the whole range**, against the batch gauge's **47.3**. The
r = 0.138 is the residue of a non-monotonic dip between inflight 4 and 11 holding fewer than 300
rows. Within this measurement's resolution the signal is uncoupled from load, and it retains 89.4
points of range for a threshold to act on. Acceptance criteria 1 and 2 close on those two numbers
together, not on the correlation alone.

**It varies between replicas at an instant, which is what the branch needs.** `Spill.targets`
excludes every replica equally under the mark, so a signal that moved as one number for the whole
fleet would be a global switch with nowhere to spill to. The spread across replicas within the same
second is 0.130 at the median and 0.141 on average.

**The grid is 0.55 / 0.62 / 0.70**, and reachability was the floor rather than the criterion. Over
the 3,510 decisions carrying a match to decline, those levels decline 8.4% / 29.7% / 72.1% of them,
and find a non-empty target set for 61.6% / 78.6% / 58.9% of those — an *effective* 5.2% / 23.3% /
42.5%, roughly even steps in the rate that actually moves a request. Below 0.55 the signal is into
a cold-start tail that recurs once per repetition; above 0.70 the majority of firings land on an
empty target set and price the rule declining to act rather than the spill. The levels live in
`bench.HitRateLowWaterGrid` with the distribution beside them in `bench.HitRateObserved`, and a test
checks each against that distribution rather than against the prose quoting it.

**What is still open.** The mark is not priced: spill was off at every cell here by construction, so
`bench.Chosen` keeps `HitRateLowWater: 0` until the sweep runs. And the distribution is a property
of this fleet at this capacity under WS 3 — a different engine version or KV capacity needs the
observing pass re-run before these levels mean anything.

## Consequences

- `Spill.KVHighWater` becomes `Spill.HitRateLowWater`; `-kv-high-water` becomes
  `-hit-rate-low-water`; `SPILL_KV` becomes `SPILL_HIT_RATE`. The reason is named for the signal
  that fired rather than for the pressure it stands for, because its predecessor was named for a
  pressure it was not measuring — and this branch has now been renamed twice on exactly that rule,
  which is the rule working rather than failing.
- The scrape is no longer optional when the branch is armed. It feeds the signal the rule routes on,
  so `-hit-rate-low-water` turns it on by itself; `-scrape-replicas` is for the observing pass,
  which runs the rule off and still needs every signal on every row. A threshold on an unscraped
  fleet would be a condition disabled for a whole run with nothing saying so, which is the shape of
  the original defect.
- One GET per replica per tick now yields both the counters and the gauge. Two scrapes of one
  endpoint on one tick doubled the request rate against the engines the measurement comes off, and
  made the two figures describe different instants — which the correlation between them may not
  have.
- Router rows gain `prompt_bytes`, `honoured_rate`, `honoured_read`, `honoured_claims` and their
  declined counterparts, and `kv_utilization` becomes `batch_kv_occupancy`. Rows written before
  this ADR carry the old column names; they are a different measurement and should not be
  concatenated with new ones under a shared name.
- The hit rate carries a span — 10 seconds and a floor of 100 block queries by default, moved by
  `-hit-rate-window` and `-hit-rate-min-queries`. Both sides are needed because the window is fed
  by a fixed scrape tick rather than by traffic: the duration bounds how far back a rate looks, and
  the floor is what stops a replica that served three blocks in ten seconds reporting a rate that
  is arithmetic rather than measurement. These are knobs of the same kind as the thresholds, not
  bounds of the kind ADR-0006 refuses to default, and every run logs the span it ran.
- The honoured rate keeps its own window — 64 requests, 30 seconds, quorum 8 by default, moved by
  `-honoured-window`/`-honoured-ttl`/`-honoured-quorum` — but nothing routes on it, so the window
  now bounds a recorded column rather than a rule.
- The honoured column only exists where the responses carry usage. A run whose client does not ask
  for `stream_options.include_usage`, or whose engines lack `--enable-prompt-tokens-details`, loses
  that comparison column but keeps the routed signal, which comes off counters the engine publishes
  by default. `cmd/spillsignal` says which is absent rather than printing a table of zeros, and
  `ops/box/run-spill-signal.sh` gates on both before it spends a cell.
- **The honoured rate conflates two ways of being wrong, and only one of them is eviction.** This
  is recorded because it is part of why that candidate was rejected, and because the column is still
  written. A replica's
  rate falls when it has evicted what the index believes it holds, and equally when the index was
  simply wrong about it — a belief kept past its TTL, a node the cap displaced. Under prefix
  affinity that is arguably what the rule should act on either way: a replica the router keeps
  being wrong about is one whose matches are not worth taking, whatever made them wrong. Under
  exact residency the index is the engines' own account, so a low rate there is eviction or a
  prefill still in flight and nothing else. The two policies therefore read slightly different
  quantities through the same threshold, which is a difference between them that is not the
  mechanism — the one thing ADR-0010 and the shared `affinityRule` exist to avoid. It is recorded
  here rather than corrected because the alternative is two rules, and it is small beside what the
  gauge was doing. The comparison between the two policies' spill counts at the same mark is where
  it would show.
- **The rule can oscillate on a replica, by construction.** A replica spilled away from receives
  fewer matches, so its cache is queried less, so its window can fall under the 100-query floor and
  report unread — and an unread rate is never under any mark, so the replica is routed to again.
  That is the self-healing property the floor gives for free, and it is also a limit cycle whose
  period is the window. It is milder than the honoured rate's version of the same cycle, because
  the counters keep moving on whatever traffic the replica still gets rather than depending on the
  router choosing to score it. Whether it costs anything is a question for the sweep: the decision
  mix and the goodput at each mark are what price it.
- **`bench.Chosen` keeps `HitRateLowWater: 0` on measurement.** `spillgrid.go` no longer promises
  that the value "becomes one of 0.55/0.62/0.70 when the run that prices them says which": the run
  says none of them. `HitRateLowWaterGrid` and `HitRateLowWaterSweep` are kept as the axis that was
  swept, not as candidates.
- **#18's pressure grid and pressure map stand as measured, at the load-only point.** The arm is
  reported beside them rather than replacing them, and no policy's cells are invalidated: a cell
  records the spill point it ran at.
- **A residency condition worth having would have to change the target rule, not the threshold.**
  Untested, and recorded as a direction rather than a decision: spilling toward the replica with
  the *highest* hit rate, rather than excluding every replica under the mark and then taking the
  least loaded, is the shape that would not degenerate on an evicting fleet. Nothing here measures
  it.
- **#16's completed grid stands as measured.** Its load axis is sound and `bench.Chosen` keeps the
  factor of 2 it chose. Its KV axis is evidence for this decision rather than a result: three
  configurations that could not fire, plus one that fired 0.14% of the time, correctly reported as
  such at the time.
- #18's separability claim was unevaluable before and is now measured: the two branches read
  signals that correlate with inflight at 0.138 and 0.979 on the same rows, so they are two
  branches. What that buys is a separate question — the residency axis has been cut but not swept,
  so #18's grid cannot be redrawn until it is.
