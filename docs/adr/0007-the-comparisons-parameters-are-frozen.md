# ADR-0007: The comparison's parameters are frozen

**Status:** Accepted · **Date:** 2026-09-08 · **TTL row amended:** 2026-09-10

## Context

The four-policy comparison has been measured three times, and twice thrown away.

1. The first run used the fixed workload, where every request is one standalone message and
   nothing is resent. There is no shared prefix there for a cache-aware policy to preserve, so
   session affinity would have scored level with round-robin and the number would have looked real.
2. The second used the multi-turn generator, and was recorded before `28421b3` shuffled the arrival
   rotation. `compare` then refused to put its open-loop cells in a table with prefix affinity's,
   because the difference between the policies would have included a difference in which
   conversation each arrival took.
3. A third was avoided only by catching it in advance: the generator declares 4 bytes per token and
   the engines report 1.66, and "correcting" that would change the workload's name and refuse every
   cell recorded under the old one.

Each time the cause was the same shape: **something the cells depend on changed after they were
recorded.** Twice the harness caught it and refused to publish a wrong table, which is the system
working — but the cost is a multi-hour re-measurement every time.

That cost is about to multiply. Three more policies land in this same table — #16's spill
configurations, #24's exact-residency policy, #26's stateless prefix hash — and #26's criteria say
so explicitly: *"It appears in the comparison table alongside the others."* Without a freeze, each
of them re-runs every policy measured before it.

## Decision

**The parameters below are frozen. Changing any of them invalidates every cell recorded under
them.** They live in one place, `lib-sweep.sh` on the GPU host, rather than being retyped per run.

### Hard-checked by `compare` — it refuses rather than mixing these

| | value |
|---|---|
| Workload | `multiturn(sessions=307,skew=0,turns=4,prompt=448t,output=64t,system=0.3x128t,branch=0.3x8fam1t,bpt=4,seed=1)` |
| SLO | TTFT < 990 ms, inter-token p50 < 24 ms, from the 2026-09-07 characterization |
| Arrival plan | shuffled rotation |

### Checked by nobody, and change the numbers silently

| | value |
|---|---|
| Cell | 150 s, 50 s warm-up, 10 s settle, 3 repetitions |
| Fleet | five replicas, GPUs 0 1 2 4 5 |
| Index node cap | fleet-sized: capacity × measured bytes-per-token ÷ 64 |
| Index TTL | 56.97 s, derived from `vllm:kv_block_idle_before_evict_seconds` |
| Spill | off — that is policy 4 as #15 defines it |

## Three of these need their reasoning recorded

**The SLO is not re-derived, though the fleet it came from no longer exists.** `floor.replicas = 6`:
it was measured before GPU 3 was dropped. It stands because the latency floor is a *solo*
measurement — one replica at a time, every other idle — and GPU 3 only throttles when all six cards
draw power at once. Its solo latency was therefore normal, and pooling it in did not inflate the
floor. Re-deriving on five cards would move the threshold slightly and change every goodput figure
ever recorded, for no gain in accuracy that anyone can point to.

**The bytes-per-token label is wrong and stays wrong.** The generator sizes prompts assuming 4 bytes
per token; the engines report 1.66, so a request carrying 5,249 prompt bytes becomes ~3,100 engine
tokens rather than the ~1,300 intended, and "working set 1.0" is really nearer 2.5. This is wrong
*identically for all four policies*, so the comparison is sound and only the label is not. The ratio
is part of the workload's name, so correcting it would refuse every existing cell. Publications
drawn from these runs must not claim WS 1.0; the correct statement is that the fleet ran well above
its cache capacity.

**The index node cap is part of the policy, not an implementation detail.** An offline replay of the
recorded rows — reconstructing every prompt from the deterministic generator and re-running the
index over them, validated to zero prompt-byte mismatches across 4,333 rows — shows the cap at 100%
occupancy at 64, 128 and 256 users. It evicts constantly, so its value shapes routing. Removing it
entirely would let the 20-second TTL alone hold ~25,300 blocks against a fleet that can hold
~16,300, so the index would believe in about 55% more than the GPUs physically have, which ADR-0006
argues is strictly worse than routing on load. It is therefore frozen at the fleet-sized derivation
and treated as part of what policy 4 *is*.

**The TTL is frozen at 56.97 s, derived.** It was not always. Deriving it needs the engines'
idle-before-evict histogram, which is empty until blocks have actually been evicted and is cleared by
a restart — so there is exactly one window where the measurement is possible, and the definitive run
of 2026-09-09 missed it. Its loop cycled the fleet after the last baseline as well as between them,
so `calibrate` scraped replicas that had been up four minutes, found the family published but empty,
and fell back to a chosen 20 s. Prefix affinity ran its whole pass against that guess. The fallback
behaved correctly — it recorded the TTL as `chosen` rather than inventing one — but ADR-0006's
premise, both bounds derived and neither chosen, did not hold for the run the project's claim rests
on.

`ops/derive-ttl.sh` now makes the derivation a step that can be run deliberately and fail loudly
rather than one line buried in a nine-hour sweep. It drives the fleet past its KV capacity and
scrapes the tail that load produced **without restarting in between**, which is the whole difficulty:
a restart clears what the load just created. Against the live fleet it pooled 5,076 observations
across all five replicas and put the p90 at **56.97 s** — nearly three times the 20 s that had been
guessed, and evidence that the guess was wrong in the direction that forfeits matches rather than the
direction that invents them.

Prefix affinity was then re-measured on both axes under the derived figure, at `node_cap=16311,
ttl=56.970757907s, ttl_source="measured from vllm:kv_block_idle_before_evict_seconds"`. Those are the
cells `runs/definitive/comparison.md` reports. The three baselines were not re-run and did not need
to be: they consult no index, so the TTL cannot reach them.

## Consequences

- The definitive run measures all four policies on both axes at these parameters, once. Every later
  ticket adds only its own cells.
- **#17 must not silently replace the node cap.** Its criterion — *"the node cap is calibrated
  against observed divergence rather than left at its initial estimate"* — is satisfied by reporting
  the calibrated cap as a *second configuration* beside this one: the fleet-sized cap gave X, the
  divergence-calibrated cap gives Y, and the difference is what the modelling assumption cost. That
  is a stronger result than a correction, and it costs no re-measurement.
- The warm-up moved from 25 s to 50 s, so cells recorded on 2026-09-08 are not directly comparable
  with the definitive run's and are superseded rather than merged. The reason is specific: prefix
  affinity's index starts empty and fills during the cell, and because every cell deliberately sends
  prompts the fleet has not seen (ADR-0004) it cannot be pre-warmed across cells. At 128 users all
  three of its cells were discarded for warm-up drift while none of session affinity's were — the
  check penalises the only policy with something to learn, and #16, #24 and #26 would each have hit
  it.
- A parameter that genuinely must change — a new model, a different fleet — invalidates this table
  and a new one starts. That is the price of the freeze, and it is smaller than the price of not
  having one.
