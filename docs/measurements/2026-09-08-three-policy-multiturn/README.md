# Three-policy comparison on the multi-turn workload — 2026-09-08

Round-robin, least-outstanding and consistent-hash session affinity, over both load axes,
on a five-replica fleet. **Session affinity roughly doubles the fleet's usable capacity**,
and the mechanism is visible at every step rather than inferred from the outcome.

Every figure below is recomputable from the cell records in this directory.

## The fleet is five cards, not six

GPU 3 is excluded. It thermally throttles whenever all six cards draw power at once — 67% of
its samples `SwThermal` against 0–34% for the rest, clocking to 960 MHz while the others hold
1305+, and worsening with time on load ([the thermal measurement](../2026-09-07-gpu3-thermal/),
[#25](https://github.com/yuchia329/kvroute/issues/25)).

It is left out because a permanently slow card **is a permanent load imbalance**, and load
imbalance is the mechanism this comparison exists to measure. It would sit inside every cell
including the skew-zero baselines; it is paid unequally by load-blind and load-aware policies,
since one keeps feeding it and the other routes away and overloads the rest; and because it
worsens through a run while each policy is a separate multi-hour sweep, it correlates with
which policy was being measured rather than cancelling across them.

That is not hypothetical. The [earlier two-policy comparison](../2026-09-08-policy-comparison/)
ran on six cards and concluded that balancing *cost* goodput — least-outstanding losing 16–32%
from concurrency 32 up. On five symmetric cards the same policy pair inverts, and
least-outstanding wins by 100–120%. The first result was an artifact of the bad card.

GPU 0 is kept despite being the next slowest: at matched load it is 3–6% off the best against
GPU 3's 12–25%, and steady rather than drifting.

## What was held constant

| | |
|---|---|
| workload | `multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1)` |
| working set | 1.0 — 307 sessions of 2,048 tokens against the 629,760 measured off these five cards |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| cells | 90 s each, 25 s warm-up, 10 s settle, 3 repetitions |
| fleet | restarted between policies, so no policy reads the caches the last one left warm (ADR-0004) |

## Closed-loop — goodput/s and prefix cache hit rate

| concurrency | round-robin | least-outstanding | session affinity |
|---:|---:|---:|---:|
| 1 | 0.62 / 6% | 0.62 / 6% | **1.22 / 60%** |
| 4 | 2.88 / 27% | 3.54 / 40% | **4.15 / 70%** |
| 8 | 3.87 / 36% | 4.97 / 46% | **6.81 / 71%** |
| 16 | 4.06 / 37% | 8.22 / 59% | **9.59 / 74%** |
| 32 | 3.29 / 32% | 7.26 / 54% | **9.90 / 74%** |
| 64 | 2.63 / 32% ⁴ | 5.56 / 51% | **7.08 / 75%** |
| 128 | 1.37 / 29% ⁴ | 0.07 / 50% | **4.17 / 78%** ⁴ |
| 256 | — ⁴ | — ⁴ | — |

⁴ Moved when the closed-loop cell records were re-judged by the rebuilt warm-up drift check
([#33](https://github.com/yuchia329/kvroute/issues/33)). Round-robin gets back a repetition each at
64 and 128 users that had been set aside as warming up and is not, and is now the median of three
at both. Session affinity at 128 users rests on one repetition: two more are cold openings. At 256
users every cell of all three policies is set aside — four as partial visits with a fleet that
slowed, since its virtual users get through too little of a conversation for every turn to land in
both halves, the rest as a fleet that slowed or a cold opening — so the row has no figure. It was
0.05 / 23% and 0.00 / 29%.

## Open-loop — the headline axis

| rate | round-robin | least-outstanding | session affinity |
|---:|---:|---:|---:|
| 2 | 1.74 / 25% † | 1.74 / 25% † | **2.00 / 63%** |
| 4 | 3.45 / 31% † | 3.37 / 29% † | **4.00 / 67%** |
| 6 | 3.90 / 29% | 3.65 / 29% | **5.98 / 69%** |
| 8 | 0.06 / 30% ⚠ | 0.00 / 31% ⚠ | **7.63 / 72%** |
| 10 | 0.00 / 31% ⚠ | 0.00 / 31% ⚠ | **9.16 / 74%** ‡ |
| 12 | 0.00 / 32% ⚠ | 0.00 / 32% ⚠ | **9.59 / 75%** |
| 14 | 0.00 / 32% ⚠ | 0.00 / 34% ⚠ | **8.84 / 77%** |
| 16 | 0.00 / 32% ⚠ | 0.00 / 32% ⚠ | 4.05 / 77% ⚠ ‡ |
| 20 | 0.00 / 29% ⚠ | 0.00 / 30% ⚠ | 3.12 / 77% ⚠ |

† Corrected by [#33](https://github.com/yuchia329/kvroute/issues/33). These four figures read `—`
and `1.74 / 23%` and `3.31 / 28%` as published, because the drift check had flagged ten of the
eighteen cells at those two rates as still warming up. They were not: re-scored from their own
rows, all ten are clear, and the figures above are the medians over all three repetitions
(round-robin 1.68–1.74 and 3.42–3.55, least-outstanding 1.68–1.75 and 3.31–3.49). See the
[re-score](../2026-09-13-warmup-drift/) and the known gap below.

⚠ Past saturation. Every cell marked ⚠ was offered more than the fleet could serve: when its
arrivals stopped, 28–99% of the requests it had been offered were still unanswered, so its queue
was still growing and its TTFT never settled. The two-sided drift check flags every such cell, and
under the rule #33 adopted it **keeps its goodput and gives up only its latency percentiles**
([ADR-0014](../../adr/0014-warm-up-drift-is-two-sided-per-turn-index-and-split-on-arrivals.md)).
Goodput is counted over the arrival window, so it is exactly the figure a past-the-knee cell exists
to report; the percentiles are a moment in a queue that had not stopped growing, and
`comparison.md` prints them as em dashes. Dropping these cells instead would blank the cache-blind
policies from rate 8 up, which is the collapse the table is here to show.

‡ Moved when the cell records were re-judged. At rate 10 one session-affinity repetition
(`a10-r3`, 5.00/s) is now flagged as a cold opening — TTFT p50 49% slower early, with 13% of its
requests unanswered at the close, under the 20% that marks saturation — so the figure is the other
two, 8.80 and 9.51, taken at their midpoint as this table takes every two-repetition median.
`comparison.md` takes the lower of two instead and prints 8.80, as it prints 3.48 at rate 6 and
8.05 at rate 14 where this table reads 3.65 and 8.84. At rate 16 the repetition that had been excluded as warming up (`a16-r3`,
6.82/s) was past saturation, and is back in. Neither moves the knee.

Both cache-blind policies are dead by rate 8: each of their six cells there left more than half
of what it was offered unanswered when arrivals stopped. Session affinity still serves 9.59 at
rate 12, with every one of its cells there keeping up.
The knee moves from about 6–7 to about 12–14: **roughly twice the usable capacity.** The
correction strengthens that rather than moving it: round-robin and least-outstanding do track
offered load at rates 2 and 4 and then collapse at 8, which is a knee at 6–7 read off four points
instead of one.

## The mechanism, not just the outcome

At concurrency 32, every link moves together:

| policy | consecutive turns on one replica | prefix hits | TTFT p50 | goodput |
|---|---:|---:|---:|---:|
| round-robin | 22.5% (chance is 20%) | 33.5% | 795 ms | 3.34 |
| least-outstanding | 47.9% | 53.9% | 487 ms | 7.19 |
| session affinity | **100.0%** | **74.1%** | **424 ms** | **9.90** |

Session affinity reaches exactly 100% — every turn of every conversation on one replica, which
is what the policy claims to do. The claim is not "policy 3 scored higher"; it is "policy 3 kept
conversations together, which raised its hit rate, which cut its prefill, which lifted its
goodput."

Least-outstanding's 47.9% is earned rather than accidental: under a closed-loop driver the
replica that just answered you has the lowest inflight, so it gets your next turn too. That is
most of why it beats round-robin here.

### The prefill column was re-read per request, and two of its verdicts inverted

`comparison.md` compares redundant prefill **per request** from
[#30](https://github.com/yuchia329/kvroute/issues/30) on. The old column was absolute recomputed
tokens, and under this closed loop that measures throughput as much as redundancy: session affinity
served 2,457 requests at 64 users against round-robin's 821, so its total rose with its own speed.

| closed loop | old absolute floor | per-request floor | session / req | the policy the old column called best |
|---|---|---|---:|---|
| 1–32 users | session affinity | session affinity | 786–1,217 | unchanged |
| 64 users | round robin | **session affinity** | 763 | round robin, at 2,181 / request |
| 128 users | least outstanding | **session affinity** | 684 | least outstanding, at 1,651 / request |

So session affinity wasted least per request at every closed-loop point it ran, where the absolute
column had it losing to a load-blind policy at the top two rungs — the exact inversion #30
describes. The narrative above ("kept conversations together, which raised its hit rate, which cut
its prefill") is what the corrected column says, and the old one contradicted it above 32 users.

The regeneration also removed three floors the old file should never have printed: at 256 users and
at open-loop 2 and 4 req/s one policy had no usable cell, so there is no floor to measure the others
against, and those cells are now em dashes. That guard post-dates the file, not the change here.

## The open-loop axis was measured twice, and the first one is wrong

`superseded-goodput/` is kept as evidence, not as a result. **Do not read it as one.**

The open-loop driver rotated arrivals through its conversation pool in a fixed order, so a
conversation's next turn was always exactly `pool` arrivals later. A round-robin router advances
one replica per arrival. The pool is arrival rate × think time — 5R against five replicas — and
5R is a multiple of five at every integer rate, so the two cancelled exactly and **round-robin
silently became session affinity**: 57.8% of consecutive turns on one replica against a 20%
chance level, and a 63% prefix cache hit rate it had not earned.

The damage was not subtle. Round-robin appeared to hold its offered rate perfectly to 10 and
turn at 12; it actually turns between 6 and 8. Least-outstanding, which sat at chance, looked
inexplicably broken beside it.

Tuning the think time does not fix this: across every value from 2.5 s to 7.5 s there is none for
which no rung of the ladder lands on a multiple of five. The rotation is now shuffled and
reshuffled every round, cells record their `arrival_plan`, and a comparison refuses to pool two
of them — the workload name cannot carry this, because it says what bytes a `(user, turn)` pair
renders to while the plan says which pair each arrival took.

**This is what the prefix-cache column was for.** Without it the symptom was only
"least-outstanding is mysteriously terrible on one axis", with nothing to say round-robin was
cheating.

## The inflight column is dead here, and the imbalance was counted instead

⚠️ **Every `session_affinity` row in this directory records `inflight` 0.** Over the three 64-user
cells that is 2,457 requests, mean 0.00, p99 0, max 0, against a fleet that was plainly not idle.
The ring stored candidates rather than replicas and is rebuilt only when the replica *set* changes,
so the load it reported was frozen at whichever request first built it — an idle fleet — and stayed
frozen for the life of the policy. Fixed in 9311e68, after every cell here was recorded
([#27](https://github.com/yuchia329/kvroute/issues/27)).

**Nothing routed differently.** The hash weighs no load by design, so every figure above stands.
What was lost is the evidence for the other half of idea.md §5: how badly a load-blind policy
leaves the fleet imbalanced. A zero in that column here means *not recorded*, never *idle*, and
any write-up quoting one is quoting an absence.

It is recoverable, because the per-request rows are the system of record and they name the replica
that served each request. Counting those — busiest replica's share of the cell's measured requests,
against the 20% fair share of five replicas, and the spread between busiest and quietest:

| concurrency 64 | busiest replica's share | busiest ÷ quietest | usable repetitions |
|---|---:|---:|---:|
| round robin | 20.0–20.2% | 1.00–1.01× | 2 |
| least outstanding | 20.7–21.6% | 1.09–1.18× | 3 |
| session affinity | **24.6–26.6%** | **1.50–2.16×** | 3 |

That is the finding the column was supposed to supply directly, and it is the stronger measurement
anyway: it asks the router for nothing but the replica it named, so no staleness in its own
bookkeeping can corrupt it. From 2026-09-11 every cell carries it as a recorded figure
(`summary.placement`), and `compare` publishes it as its own section. Cells in this directory carry
no such block, which is the truth about them — nobody counted their placements — and the report
prints an em dash rather than a balanced-looking zero.

⚠️ One caveat on the numbers above: they are per repetition, and the busiest replica is not the same
card in each. Pooling the three 64-user repetitions into one count instead gives session affinity
605 requests on the busiest replica against 358 on the quietest — a 1.69× spread, lower than any
per-repetition figure, because a run that piled onto replica-2 and a run that piled onto replica-5
cancel. Per repetition is what is published, because each repetition is a separate run on a fleet
that was restarted, and the union of three runs is not a fleet that ever existed.

## Known gaps, stated rather than smoothed

- ~~**14 of 153 closed-loop cells and the open-loop rates 2 and 4 are flagged as still warming
  up.**~~ **Corrected by [#33](https://github.com/yuchia329/kvroute/issues/33): the open-loop rates
  2 and 4 were not warming up.** The check that flagged them pooled the two halves of the measured
  window, and at rate 2 a visit period is 20 s against a 65 s measured window — 3.25 periods, so
  each half drew a different mix of turn indices. Compared within each turn index the 43–68%
  "slowdown" collapses to between −0.14 and +0.22, and all ten of those cells are clear. Their
  goodput was recorded all along: round-robin **1.74** at rate 2 (1.68–1.74, n=3) and **3.45** at
  rate 4 (3.42–3.55, n=3), and least-outstanding **1.74** (1.68–1.75) and **3.37** (3.31–3.49),
  against offered 2.00 and 4.00 — so both cache-blind policies do track offered load below their
  knee. The open-loop table above is corrected accordingly.
  The closed-loop half of the claim mostly survives: re-scored from the rows, 7 of the 72 cells in
  `concurrency/` were flagged and 13 are now, six of them still as a cold opening. The correction is
  to the cause and the remedy, not to the fleet.
  The re-score of all 216 recorded open-loop cells is
  [here](../2026-09-13-warmup-drift/), and its verdicts are now written into the open-loop cell
  records, so `comparison.md` and the figures are regenerated from them. The closed-loop records in
  `concurrency/` still carry the verdicts they were recorded with.
- **No policy has a usable cell at concurrency 256** since the re-score; session affinity never did.
- **The derived-identity path is not exercised.** The harness always sends `X-Session-Id`, so
  every cell here measures the supplied-key oracle. idea.md §4.2 wants the derived key measured
  too; no sweep flag suppresses the header yet.
- **Replica-set change is proven by unit test, not on the fleet.** Losing a replica mid-run is
  [#19](https://github.com/yuchia329/kvroute/issues/19)'s job.

## Files

| | |
|---|---|
| `comparison.md` | the generated table: kept closed-loop cells plus corrected open-loop cells. Regenerated for [#30](https://github.com/yuchia329/kvroute/issues/30), which moved the prefill columns, and again for [#33](https://github.com/yuchia329/kvroute/issues/33), which re-judged the open-loop cells and brought in #27's placement section |
| `concurrency/`, `goodput/` | cell records and compacted parquet for the two axes |
| `superseded-goodput/` | the first open-loop pass, kept as evidence of the rotation artifact |
| `evidence/` | run logs, and the engine's prefix-cache counters before and after each pass |
| `router-*.jsonl.gz` | the router's own per-request rows, joinable to the harness rows by request id |
