# The spill rule's residency signal — 2026-09-12

#28 found that the spill rule's two conditions were one condition in two units. The branch that
was supposed to answer memory pressure read `vllm:kv_cache_usage_perc`, which counts the blocks
held by a replica's *running* batch — the same thing the load branch measures — and over #16's
13,658 rows the two agreed at r = 0.973. Two of the three levels swept could not fire at the
concurrency they ran at.

This is the run that replaced the signal and priced the replacement. It is an **observing pass**:
the spill rule is off at every cell, so the signal is seen over its natural range rather than over
the range its own spilling would produce.

**The residency branch now reads the engines' own per-replica prefix cache hit rate, and it is
separable from load.** On 4,022 decisions it moved with inflight at **r = 0.138**, against
**r = 0.979** for the batch gauge on the same rows, at the same instants.

**The correlation understates how separable it is, and the effect size is the honest number.**
Across the whole load range the hit rate's mean moves **0.6 percentage points**; the batch gauge's
moves **47.3**. So this is not a smaller coefficient on a comparable signal — the underlying
movement is roughly eighty times smaller, while the hit rate still spans 89.4 points of range for a
threshold to bite on.

**It is a per-replica signal, not a fleet-wide one.** At any given second the five replicas' hit
rates differ by **0.130 at the median** and 0.141 on average. That is what makes it usable: the
residency branch excludes every replica equally under the mark, so a signal that moved as one
number for the whole fleet would leave nowhere to spill to.

**The grid is 0.55 / 0.62 / 0.70**, cut against the observed distribution and against the rate at
which each level would actually move a request. See [The grid, and how it was cut](#the-grid-and-how-it-was-cut).

The second candidate the ticket recommended — honoured belief — was built, measured here, and
rejected. It is reported below as a column rather than deleted, because the reason it fails is
structural and worth not rediscovering.

## What was held constant

| | |
|---|---|
| policy | `prefix_affinity`, **spill off at every cell** — this is the observing pass |
| workload | `multiturn`, WS **3**, skew **0** — `bench.KVPressureWorkingSet` and `KVPressureSkew`, the residency axis's own point |
| geometry | 4 turns, 448 new prompt tokens a turn, 64 output, 30% shared system prompt, 30% branched, `seed=1`, KV capacity 629,760 |
| load | closed loop, 32 virtual users |
| cells | 150 s, 50 s warm-up, 10 s settle, **3 repetitions** |
| scrape | 250 ms tick, hit-rate span **10 s / 100 queries minimum** |
| fleet | five cards, GPUs 0 1 2 4 5 — GPU 3 is out (#25) |
| engine | vLLM 0.28.0, `awq_marlin` / `marlin`, block 16, max model length 8192, GPU memory 0.9, prefix caching, **KV cache events off** — [`evidence/versions.env`](evidence/versions.env) |
| binaries | `router` md5 `1e6300548f02…`, `bench` `d6836d201b9d…`, `spillsignal` `7df890179eb7…` |
| driver | [`ops/box/run-spill-signal.sh`](../../../ops/box/run-spill-signal.sh) |

WS 3 offers three times the session tokens the fleet can hold, so the replicas evict continuously —
the condition the signal has to be able to see. Skew 0 so that nothing but memory pressure moves.

Ran 2026-09-12 19:43 → 19:57 UTC. All **4,022** requests succeeded; zero reroutes, zero drops,
zero failures.

## The three signals, on the same rows

Every row carries all three, taken from one GET per replica per tick — one scrape rather than
three, so the numbers describe the same instant. That is what makes the correlation between them
a fair reading rather than two time series aligned after the fact.

| signal | observed range | correlation with inflight |
|---|---|---|
| **prefix cache hit rate** (the residency branch) | 3,955 read, 67 unread: min 0.014, p10 0.560, p50 0.662, p90 0.735, max 0.908 | **r = 0.138** over 3,955 pairs |
| honoured rate (measured, not routed on) | 3,927 read, 95 unread: min 0.948, p10 0.982, p50 1.000, p90 1.000, max 1.000 | r = −0.581 over 3,927 pairs |
| batch KV occupancy (not routed on) | 4,022 read, 0 unread: min 0.000, p10 0.009, p50 0.066, p90 0.583, max 0.848 | **r = 0.979** over 4,022 pairs |

The batch gauge reproducing #16's r = 0.973 as **0.979** on a different run, a month later, is the
diagnosis confirming itself. That was the control, and it behaved.

### Correlation is the weaker half of the claim

An r on 4,000 rows is significant long before it is meaningful, and this project has already been
misled once by reading a coefficient as an effect — the honoured rate's r = −0.702 turned out to
be a real slope across a range 1.7 points wide. So the same question is asked of every signal here
in the units the rule actually uses:

| inflight | hit rate (mean) | batch KV (mean) |
|---|---:|---:|
| ≤ 1 | 0.659 | 0.030 |
| ≥ 6 | 0.654 | 0.503 |
| **moved by the load range** | **0.6 points** | **47.3 points** |

The hit rate is not weakly coupled to load. Within measurement resolution it is uncoupled, and the
r = 0.138 is the residue of a non-monotonic dip between inflight 4 and 11 where fewer than 300 rows
sit. The batch gauge, on the same rows, is very nearly a linear readout of the load branch's own
input.

### And the signal has to vary between replicas, not just over time

A residency mark is a per-replica question — "has *this* replica stopped holding what we think it
holds" — and `Spill.targets` excludes every replica equally under the mark, so a fleet that moved
as one number would have nothing to spill onto. Measuring the spread across replicas within the
same second:

| | spread across replicas in one second |
|---|---:|
| min | 0.013 |
| p10 | 0.064 |
| **p50** | **0.130** |
| p90 | 0.221 |
| max | 0.817 |

Thirteen points of separation at the median. The signal distinguishes replicas at an instant, which
is the property the branch is built on.

## Why the honoured rate was rejected

It was the ticket's recommended candidate and the truest signal available in principle: compare
`usage.prompt_tokens_details.cached_tokens` against the match the router predicted for that
request, and a replica that stops honouring the index's beliefs is a replica that is evicting.

It saturates. p50 = p90 = max = 1.000, and 70% of readings in the first run were exactly 1.0. The
cause is structural rather than a tuning problem: **the prefix index is calibrated not to
over-predict** (ADR-0006, ADR-0008), so it gives up beliefs before the engines evict the blocks,
and is therefore nearly always right about whatever it still claims. The signal measures the
index's own conservatism, not the replica's residency.

That was confirmed off the GPU, at zero fleet cost, by replaying this run's real `(claimed, held)`
pairs through the real accumulator at five window lengths: the histogram widened **2.4×** as the
window shortened while the predictive gap — the honoured share of the *next* request after a
bottom-fifth reading versus a top-fifth one — stayed flat at about **1.6 points**. Widening the
spread of a signal that predicts nothing better is not progress. The replay lives in
`internal/belief/replay_test.go` and runs opt-in against a recorded run.

Its r of −0.581 here is the same trap in a smaller font: a real coefficient over a range 5.2 points
wide, of which the bottom 4.8 are a tail below p10.

## The grid, and how it was cut

Reachability is #28's stated criterion — the signal must have been observed below every level — and
all three levels clear it against a minimum of 0.014. But reachability alone is what #16's 0.70
passed while still firing on 0.14% of decisions, so the levels were chosen against two further
things: how often a mark declines a match, and whether the decline finds anywhere to go.

Over the 3,510 decisions that carried a prefix match to decline:

| mark | declines | of those, a target existed | **effective** | firings that are no-ops |
|---|---:|---:|---:|---:|
| 0.50 | 5.8% | 62.4% | 3.6% | 37.6% |
| **0.55** | 8.4% | 61.6% | **5.2%** | 38.4% |
| 0.60 | 21.3% | 75.4% | 16.1% | 24.6% |
| **0.62** | 29.7% | 78.6% | **23.3%** | 21.4% |
| 0.65 | 41.9% | 74.2% | 31.1% | 25.8% |
| 0.68 | 60.8% | 65.1% | 39.6% | 34.9% |
| **0.70** | 72.1% | 58.9% | **42.5%** | 41.1% |
| 0.72 | 84.8% | 45.3% | 38.4% | 54.7% |
| 0.75 | 93.8% | 23.7% | 22.2% | 76.3% |

*Effective* is the share of matched decisions where the best replica is under the mark **and** some
other replica is not, so `targets()` is non-empty and the request actually moves. Where the whole
fleet is under, the set is empty and the rule keeps the match by design — correct behaviour, but a
level whose firings mostly land there prices the empty set rather than the spill.

**0.55 / 0.62 / 0.70** spans 5.2% → 23.3% → 42.5% effective, roughly even steps in the rate that
matters, with every point spending the majority of its firings actually moving a request.

The ends are where they are for a reason. Below 0.55 the signal is into its cold-start tail: the
readings under 0.50 are 5.5% of the run and cluster at the first seconds of each repetition — the
minimum of 0.014 recurs at t≈0, 120 and 240 s, once per rep — so a level down there measures
warm-up. Above 0.70 the no-op share takes over, reaching 76.3% by 0.75.

These levels are written into `bench.HitRateLowWaterGrid` with the distribution beside them in
`bench.HitRateObserved`, and a test checks each level against that distribution rather than against
the comment quoting it.

## Graceful degradation, as it happened

67 rows of 4,022 (**1.7%**) carried no hit-rate reading, spread through the run from 37 s to 325 s
rather than bunched at the start. They are the quorum working: a replica that served fewer than 100
block queries in the trailing 10 s has a rate that would be arithmetic rather than measurement, and
it is reported unread. An unread reading is never `Under()` any mark, so those decisions kept their
match — the same rule the gauge this replaced carried, in the one place a policy cannot forget it.

## What this run does not say

It does not price the residency mark. Spill is off at every cell here by construction, so there is
no goodput consequence in these numbers and `bench.Chosen` still carries `HitRateLowWater: 0`.
Sweeping 0.55/0.62/0.70 at this workload point is the next run, and until it happens the residency
condition stays off in the settled policy.

It also does not transfer to another fleet. The distribution is a property of five 3090s at this KV
capacity under WS 3, and the levels are cut from it; a fleet with different capacity or a different
engine version needs the observing pass re-run before these levels mean anything. The driver script
says so at the top.

## Reproducing

```
make box-sync                      # flips KV_EVENTS to 0 as a side effect
ssh nlp 'cd ~/kvroute && ./run-spill-signal.sh'
```

The script gates on all four preconditions before it spends a cell: an empty run directory (bench
resumes by cell id, and these ids carry no signal in them, so a second observing pass of a
*different* signal would silently report the first one's cells), `KV_EVENTS=0`, every replica
reporting per-request cached prompt tokens, and every replica publishing
`vllm:prefix_cache_queries_total`. Each failure is silent otherwise, and each would produce a report
describing a measurement that did not happen.

Raw rows: [`evidence/router-prefix_affinity.jsonl.gz`](evidence/router-prefix_affinity.jsonl.gz),
4,022 decisions with all three signals on each. Report as generated:
[`spill-signal.md`](spill-signal.md).
