# NUMA contention under simultaneous load — 2026-09-07

Two characterizations of the same fleet, both with all six replicas **driven at once** so that each
one's host-side work competed with the others'. The earlier characterization drove replicas one at a
time, which structurally removes the contention this pair exists to find.

| | pass 1 | pass 2 |
|---|---|---|
| directory | [`unpinned/`](unpinned/) | [`pinned/`](pinned/) |
| CPU pinning | off — the fleet as it has run all along | `CPU_PINNING=1`, 12 threads per replica |
| everything else | identical: `-schedule together -levels 1,32`, 90s probes, 25s warm-up, 3 repetitions | |

Each pass got a fresh fleet, so no replica had seen the prompts before its own pass began.

## What the two passes were for

The host is not symmetric. GPUs 0–3 hang off NUMA node 0 and GPUs 4–5 off node 1, and each node has
48 hardware threads — so node 0's four cards have 12 threads each to node 1's 24. Pass 1 asks
whether that shows up as a latency difference when every card is busy at once. Pass 2 removes the
difference by construction, binding every replica to 12 threads of its own node, and asks what
changes.

## Result

**At concurrency 32 with pinning, the node comparison settles: the two nodes are 5.5% apart, inside
the 8% tolerance, against a repeat noise of 6.8%.** That is the only cell in either pass that
resolved anything. Everywhere else the spread between things is smaller than the spread of a thing
against itself, which is not a finding in either direction.

| | c1 unpinned | c1 pinned | c32 unpinned | c32 pinned |
|---|---:|---:|---:|---:|
| TTFT spread between replicas | 16.0% | 17.2% | 16.9% | 19.0% |
| …against repeat noise of | 19.6% | 21.7% | 24.9% | 22.0% |
| per-replica question | unresolved | unresolved | unresolved | unresolved |
| inter-token spread | 5.7% | 6.0% | 16.0% | **3.0%** |
| TTFT spread between nodes | 7.2% | 7.6% | 6.7% | **5.5%** |
| …against repeat noise of | 11.9% | 12.6% | 13.9% | **6.8%** |
| node question | unresolved | unresolved | unresolved | **resolved, symmetric** |

Node means, TTFT p50:

| | node 0 (4 cards) | node 1 (2 cards) | gap |
|---|---:|---:|---:|
| c1 unpinned | 364ms | 340ms | 7.2% |
| c1 pinned | 365ms | 339ms | 7.6% |
| c32 unpinned | 1248ms | 1170ms | 6.7% |
| c32 pinned | 1236ms | 1172ms | 5.5% |

Node 0 is consistently the slower of the two, in all four cells, by 5.5–7.6%. The sign never
changes. Only the last cell can tell that gap from its own noise.

## Reading it

**Pinning halved the node-level noise at load, and that is what settled the question.** Between
repetitions a node's own mean moved 13.9% unpinned and 6.8% pinned at c32. The gap between the nodes
barely moved (6.7% → 5.5%); what changed is how well it could be seen. The inter-token spread fell
the same way, 16.0% → 3.0%, which is a second statistic pointing in the same direction.

> **Withdrawn, 2026-09-07.** Do not cite the noise-halving above. It rests on a range statistic over
> three repetitions, compared across two passes that are *separate runs* — and the fleet's own floor
> moved 320 ms → 329 ms → 344 ms across the bring-up, solo and contention runs, a **7.5% between-run
> drift** comparable to the 8% tolerance itself. That drift sits underneath the 13.9% → 6.8%
> comparison and is large enough to produce it on its own. The claim is unsupported, not merely
> unreplicated. What survives is the narrower statement that the two nodes are 5.5% apart at
> concurrency 32 under pinning, inside the tolerance.

**Pinning did nothing at concurrency 1**, as it should. Per-replica figures are near identical
across the two passes — replica-0 measured 362.7ms unpinned and 363.0ms pinned — because one
request at a time barely occupies a CPU, so a 12-thread budget is not a constraint.

**Pinning did not settle the per-replica question** at either level. Per-replica repeat noise stayed
between 19.6% and 24.9% in both passes. Whatever makes one replica differ from another is not the
CPU budget.

**Replica-3 is persistently the slowest**, in both passes and at both levels: 391ms and 395ms at c1
against a fleet best of 337ms, and 1275ms and 1321ms at c32. It also completes visibly fewer
requests — 222–226 against ~245 at c1, and 419–426 against ~500 at c32.

> **Followed up, 2026-09-07, and explained.** This was recorded above as a lead inside the noise.
> It is a result. Bucketing every request into 5-second slices shows replica-3 slowest in **38 of
> 41** slices unpinned and **39 of 41** pinned at concurrency 1 — stable, not noise. At concurrency
> 32 the slowest replica rotates (replica-3 slowest in 16 slices, fastest in 12), so *that* spread
> genuinely is noise.
>
> The cause is **thermal throttling on GPU 3**, measured in
> [the follow-up](../2026-09-07-gpu3-thermal/): it is the only card predominantly limited by heat
> (SwThermal in 67% of samples against 0–34% for the rest), clocking down to 960 MHz while the
> others hold 1305 MHz or better. It appears only when all six cards draw power at once, which is
> why the solo characterization and the bring-up run both found the fleet symmetric to ~2%, and why
> pinning did not touch it.

## Caveats, which are not small

- **The c32 probes are under-warmed in both passes.** 5 of 36 probes were flagged in pass 1 and 7 of
  36 in pass 2, all at c32, all "still warming up" with the first half of the measured window 30–32%
  slower than the second against a 25% threshold. A 25s warm-up is not enough when six replicas
  compete. Both passes carry the defect equally, so the comparison between them survives it, but the
  absolute c32 numbers should not be quoted.
- **One pass per condition.** The noise halving is large and corroborated by a second statistic, but
  it is not replicated. A second pinned pass would be needed to call it a property of pinning rather
  than of that particular twelve minutes.
- **The "threads per GPU" column in `pinned/report.md` reads 12 and 24, which is wrong for that
  pass.** The tool derives it from the host topology and knows nothing about pinning; under
  `CPU_PINNING=1` every replica had 12. Reading the replicas' actual CPU affinity would fix it.

## The pinning bug this run found

The first pinned bring-up assigned **the same** threads 0–11 to all four of node 0's replicas and
24–35 to both of node 1's. `take_threads` returned the first n threads of a card's local CPU list,
and every card on a node reports the same list, so the sets overlapped completely: four replicas
sharing twelve threads where they were meant to have twelve each — less CPU than they get unpinned,
and still unequal between the nodes.

Pass 2 was run after the fix. The share is now keyed on a replica's ordinal among its own node's
cards and taken evenly from each range of the list, because the two ranges are the node's physical
cores and then their SMT siblings (cpu0's sibling is cpu48, cpu12's is cpu60). Every replica gets six
whole cores and those same cores' siblings:

| replica | GPU | node | threads |
|---|---:|---:|---|
| `replica-0` | 0 | 0 | 0-5, 48-53 |
| `replica-1` | 1 | 0 | 6-11, 54-59 |
| `replica-2` | 2 | 0 | 12-17, 60-65 |
| `replica-3` | 3 | 0 | 18-23, 66-71 |
| `replica-4` | 4 | 1 | 24-29, 72-77 |
| `replica-5` | 5 | 1 | 30-35, 78-83 |

Verified against the live processes with `taskset -cp` before pass 2 started. Node 1 is left with 24
of its 48 threads idle, which is the price of equality.

## What did not change

The latency floor and the SLO derived from it are the same in both passes, and the same as the
solo characterization: TTFT p50 **344ms** unpinned and **343ms** pinned, giving an SLO of 1.04s and
1.03s at the 3× multiple. Fleet KV capacity read 125,952 tokens on every replica in both passes.
Prefix hit rate was 1.3% throughout, against the 5% limit, so these are prefill costs.

## Reproducing

```sh
make linux && rsync -az bin/characterize-linux-amd64 nlp:kvroute/bin/ && rsync -az ops/ nlp:kvroute/ops/

# pass 1
ops/fleet.sh up
bin/characterize-linux-amd64 -dir runs/contention -schedule together -levels 1,32 \
  -model "$(ops/fleet.sh env MODEL)" -gpus "$(ops/fleet.sh env REPLICA_COUNT)" \
  -replicas "$(ops/fleet.sh replicas)"

# pass 2
ops/fleet.sh down
CPU_PINNING=1 ops/fleet.sh up
bin/characterize-linux-amd64 -dir runs/contention-pinned -schedule together -levels 1,32 \
  -model "$(ops/fleet.sh env MODEL)" -gpus "$(ops/fleet.sh env REPLICA_COUNT)" \
  -replicas "$(ops/fleet.sh replicas)"
```

Either report can be rebuilt from its own rows without the GPUs:

```sh
bin/characterize -render docs/measurements/2026-09-07-numa-contention/pinned
```
