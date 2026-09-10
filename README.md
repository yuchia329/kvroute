# kvroute

A KV-cache-aware inference router fronting six single-GPU vLLM replicas.

The question it exists to answer: **where does cache-aware routing stop paying for itself against
the consistent-hash session-affinity baseline any load balancer already gives you for free?** The deliverable is a
measured comparison of routing policies, not a service. See [`idea.md`](idea.md) for the spec and
[`CONTEXT.md`](CONTEXT.md) for the vocabulary.

> **Status: the first policy comparison is measured.** Six replicas come up behind a preflight, the
> fleet's KV capacity, its latency floor and its SLO are measured rather than assumed, and every cell
> lands as a row with its own contamination evidence. Two of the four policies are in — round-robin
> and least-outstanding — and they have now been run head to head over both load axes. What is
> missing is the rest of the comparison: the two cache-aware policies and both pressure axes.
>
> **Verified on the box, 2026-09-06/07.** All six replicas came up under `ops/fleet.sh` behind the
> preflight, the metric contract passed against every one of them, and a characterization pass drove
> each replica on its own for 36 probes and 6,701 requests, all clean. The numbers below are from
> those runs. See [ADR-0001](docs/adr/0001-engine-pin-and-forced-kernel-selection.md) for what first
> contact corrected and [ADR-0004](docs/adr/0004-every-measurement-sends-unseen-bytes.md) for the
> measurement that nearly went in seven times too low.

## First result: balancing load cost goodput

[Round-robin against least-outstanding](docs/measurements/2026-09-08-policy-comparison/), 90 cells,
~122,500 requests, 2026-09-07/08. Goodput is requests per second meeting the derived SLO of
**TTFT < 990 ms and inter-token p50 < 24 ms**.

| concurrency | round_robin | least_outstanding | Δ |
|---:|---:|---:|---:|
| 8 | 7.50 | **8.01** | **+6.8%** |
| 16 | 10.34 | **10.56** | **+2.2%** |
| 32 | **12.34** | 10.28 | **−16.7%** |
| 64 | **9.80** | 8.21 | **−16.2%** |
| 128 | **9.44** | 6.41 | **−32.1%** |

**Least-outstanding completed more requests than round-robin at every level — up to 28% more — and
met the SLO on fewer of them.** The crossover sits between 16 and 32 virtual users.

The mechanism is one bad card. GPU 3 thermally throttles under sustained six-card load
([#25](docs/measurements/2026-09-07-gpu3-thermal/)). Round-robin sends it exactly its sixth and lets
the queue pool there: at concurrency 128 that replica sits at **6.7 s TTFT and passes nothing**,
while the other five run at ~370 ms and pass essentially everything. Least-outstanding sees the
inflight climbing, routes away — 14.70% of traffic against round-robin's 16.67% — and so moves that
pressure onto the healthy five, lifting them from ~370 ms to ~1100 ms, across the threshold. A
bimodal fleet beat a balanced one because **goodput counts requests under a threshold, and
balancing moved the whole distribution over it**.

That ordering is a property of a fleet with one slow member, not a verdict on the policy: on a
symmetric fleet there is no sacrificial queue to exploit. Quote the −32% only with GPU 3 attached.

## Measured facts

Six replicas, `Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, one per RTX 3090. Everything here is measured
on the box, and every figure is recomputable from rows kept in the repo.

| Quantity | Measured | Note |
|---|---|---|
| **Fleet KV capacity** | **755,712 tokens** | 7,872 `num_gpu_blocks` × 16 per replica, identical on all six, read off each replica's own `/metrics`. 9.8% above the hand estimate — the whole gap is the KV-budget assumption, not the per-token arithmetic |
| **Hardware latency floor** | **TTFT p50 329 ms, inter-token p50 7.8 ms** | 1,487 requests at concurrency 1, straight at each replica, pooled. Measured at a 1.3% prefix-cache hit rate, which is what makes it a prefill cost rather than a cache lookup |
| **SLO** | **TTFT < 990 ms, inter-token p50 < 24 ms** | 3× the floor, rounded up, published with the alternatives — [ADR-0003](docs/adr/0003-slo-is-three-times-the-measured-floor.md) |
| **Working set ratios** | **92 / 369 / 1,107 / 2,952** sessions of 2k | WS 0.25 / 1 / 3 / 8, rescaled off the measured capacity |
| **Replica symmetry** | **1.5–2.3% spread at concurrency 1** when replicas are driven one at a time; **12.7%** when all six are driven at once | the fleet is symmetric at rest and is not under load: GPU 3 throttles thermally. See below |
| Host topology | GPUs 0–3 on NUMA 0 at 12 threads each, GPUs 4–5 on NUMA 1 at 24 | pairs `PIX`, within-socket `NODE`, across-socket `SYS` |
| Router overhead p50 / p99 | **135 µs / 318 µs** | 280 requests. Against the < 1 ms p99 Phase 1 gate — passes, though the max of 1.34 ms includes first-connection setup |
| Contamination | zero foreign processes across 36 probes and two sweep cells, `clean: true` | the ownership check works: the process holding each card is *not* the pid in the pid file |

⚠️ **The fleet is not symmetric under load, and the cause is heat.** Driven one replica at a time
the six agree to 1.5–2.3%, across three independent runs. Driven all six at once the spread is
12.7% and it *grows with time on load* — replica-3 sits 1.1% off the fleet minimum in the first
repetition, 12.7% in the second, 17.2% in the third.

GPU 3 is the only card predominantly limited by **heat** rather than by the normal, equal 280 W
power cap: `SwThermal` in 67% of samples against 0–34% for the other five, clocking down to 960 MHz
while the rest hold 1305 MHz or better — and doing so at 75 °C, cooler than the 83 °C GPU 4
tolerates without throttling at all. NUMA is ruled out: the effect does not follow the node
boundary, and pinning every replica to twelve disjoint local threads did not touch it. See
[the measurement](docs/measurements/2026-09-07-gpu3-thermal/) and [#25](https://github.com/yuchia329/kvroute/issues/25).

This matters more than its 2.8% of fleet capacity suggests, because the policies are not equally
exposed to it: the two load-aware policies automatically route away from a replica that accumulates
inflight, while round-robin and consistent-hash keep feeding it. That is a free, always-on instance
of the very load-imbalance mechanism the experiment exists to measure — sitting inside the cells
meant to be the baseline.

The **symmetry check in the spec cannot see this**, because it prescribes driving each replica
alone, which is the one configuration in which the effect cannot appear. `make contention` drives
all six at once and is what surfaced it.

✅ **The capacity discrepancy is explained.** [ADR-0001](docs/adr/0001-engine-pin-and-forced-kernel-selection.md)
recorded 119,408 tokens at first contact against 125,952 since. It is the `torch.compile` cache, and
it reproduces to the token: a replica started with `VLLM_CACHE_ROOT` moved aside gives **119,408
tokens with 19.2 s of compilation**, against 125,952 with 0.26 s warm. vLLM sizes its KV cache from
what is free after profiling, and a cold compile is still holding ~0.8 GiB while that runs. So KV
capacity is not a property of the engine settings alone, and it is read off every replica at runtime
on every run.

The rows behind every figure are kept in
[`docs/measurements/`](docs/measurements/) — the [characterization](docs/measurements/2026-09-07-characterization/),
the [fleet bring-up](docs/measurements/2026-09-06-fleet-bringup/) and
[belief divergence](docs/measurements/2026-09-10-belief-divergence/), each with per-request JSONL,
the record, and each replica's own startup log. Sweep output is gitignored because a full pass is
hundreds of megabytes; a reference run the README cites is not, because a figure whose rows have
been deleted is an assertion rather than a measurement.

## What runs today

```
cmd/bench ──► router (:8080) ──► replica-0..5 (:8000..:8005, one GPU each)
    │             │                   │
    │             │                   └─ /v1/chat/completions (SSE), /metrics
    │             └─ round-robin and least-outstanding policies, exact per-replica
    │                inflight counters, per-request JSONL rows, own-overhead percentiles
    └─ closed-loop and open-loop drivers, per-cell caching, nvidia-smi
       contamination sampling, JSONL during the run → Parquet after

cmd/compare ──► the cell records the sweeps wrote, no fleet in the path
    └─ each policy's goodput against the derived SLO at each load point, with the
       spread across repetitions beside it

cmd/divergence ──► the per-request rows the sweeps wrote, no fleet in the path
    └─ the router's prefix match against the engine's own cached-token account for
       the same request, binned by working set ratio and by how long it had been
       since that conversation was last served, over- and under-prediction apart

cmd/characterize ─────────────► replica-0..5, one at a time, no router in the path
    └─ fleet KV capacity off every replica, host topology, the latency floor,
       the SLO derived from it, and the replica symmetry verdict
```

- **`cmd/router`** — OpenAI-compatible `POST /v1/chat/completions` with SSE passed through
  untouched, one JSONL row per request, and its own accept-to-dispatch cost reported at
  `GET /router/stats`. It counts each replica's inflight itself, exactly, rather than
  scraping it: the router is the sole ingress, so it knows what it dispatched, and a figure that
  came from a 250 ms–1 s scrape would read the same for every request arriving inside one window —
  so they would all pick the same least-loaded replica and stampede it. That is the classic stale
  load-balancer failure, and it would have quietly corrupted both load-aware policies. `-policy`
  selects the rule: `round_robin` distributes evenly with no state, `least_outstanding` takes the
  replica holding the fewest inflight requests and breaks ties by rotation, because every replica of
  an idle fleet is tied and a fixed tie-break would send the whole low-concurrency end of the sweep
  to one card. Every row records the inflight the decision was weighed on, so how balanced a policy
  left the fleet is a figure in the data rather than a claim about the code. `session_affinity`
  hashes a conversation onto a ring of the replicas, and `prefix_affinity` routes to the replica
  already believed to hold the longest leading run of the prompt's own bytes — it needs
  `-prefix-calibration`, because the index's node cap and TTL are measured off the fleet rather
  than defaulted in the source, and a router asked for that policy without one refuses to start
  ([ADR-0006](docs/adr/0006-the-prefix-index-is-bounded-by-measurement.md)). Every row records the
  prefix match the decision was made on, in bytes, alongside the measured prompt bytes-per-token
  ratio that converts it — the router carries no tokenizer, and reporting a router-side belief in
  the engine's units would hide that.
- **`cmd/calibrate`** — measures the prefix index's two bounds off the fleet and a sweep that has
  already run: aggregate KV capacity summed from every replica's own cache configuration, the
  prompt bytes-per-token ratio from the cells' offered bytes over the engines' reported tokens, and
  the TTL from the p90 of `vllm:kv_block_idle_before_evict_seconds`. It writes them to a file, and
  refuses rather than substituting a constant for a measurement it could not take. With
  `-divergence` it also folds in what a completed sweep measured of the index's own accuracy, and
  the node cap becomes the fleet model scaled by the share of its belief the engines honoured —
  the check [ADR-0006](docs/adr/0006-the-prefix-index-is-bounded-by-measurement.md) said it could
  not make itself.
- **`cmd/bench`** — the two load drivers and the sweeps they run. Refuses to start unless every
  replica answers `/health`, warms each one directly, then drives one of two axes, counting dropped,
  failed and SLO-violating requests in three separate columns. Samples the GPUs throughout and
  resumes from cached cells. Every table it writes names the driver behind it, because the two are
  not comparable:
  - `-driver closed_loop` (the default) holds a fixed number of virtual users at each of eight
    concurrency levels. Offered load is an outcome, so the tail is optimistic by construction — the
    fleet slows, the driver slows with it, and the knee is never reached. That is the right shape
    for a *scaling* axis and the wrong one for goodput.
  - `-driver open_loop` fires on a fixed arrival schedule at each rate of a ladder, whether or not
    earlier requests have finished. Offered load is an input, so this is where the headline goodput
    number comes from. Every row records when it was *due* beside when it was sent, and a cell whose
    driver fell behind its own schedule is flagged rather than reporting a rate the fleet was never
    offered — [ADR-0005](docs/adr/0005-open-loop-fires-on-schedule-and-records-its-own-lateness.md).
    Arrivals rotate through a pool of conversations, `-think-time` wide by the arrival rate, so
    sessions advance a turn at a time as they do under the closed-loop driver: the two offer traffic
    of one shape and differ only in what paces it.
- **`cmd/characterize`** — establishes the measured facts every other number scales off, in one
  pass: aggregate KV capacity read off all six replicas' own `num_gpu_blocks`, the host's GPU
  topology and NUMA placement, the hardware latency floor, the SLO derived from that floor as a
  stated multiple, and whether the six replicas are interchangeable. It drives each replica
  directly, because every question it answers is about a replica and the router's policy would
  otherwise be in the answer. Two schedules: `-schedule solo` drives one replica with the rest idle,
  which isolates the card, and `-schedule together` drives all six at once, which is the only
  condition in which whole-fleet contention exists — for cores, when four cards share a NUMA node,
  and for cooling and power, which is what actually bites on this host. Comparing the two schedules
  is what found GPU 3's thermal throttling: invisible solo, 12.7% together.
  `-render <dir>` rebuilds the analysis and the report from a
  finished run's own rows, so a corrected definition costs no GPU time and a published figure is
  never one no committed code can produce.
- **`cmd/compare`** — the table the project's claim is made in: each policy's goodput against the
  derived SLO at each point of the load axis. It reads the cell records only, so a comparison
  rebuilds from a checkout with no fleet running. It refuses rather than renders when the cells
  cannot honestly be put side by side — a different SLO on either side, a workload seeded
  differently, or a cell with no SLO at all and therefore no goodput. Each figure is the median of
  its repetitions with the range across them, a difference smaller than those ranges is labelled as
  being inside the spread rather than left to read as a result, and a flagged or unclean cell is
  excluded and listed rather than averaged in.
- **`cmd/divergence`** — how wrong the router's index was, per request. The prediction is the
  prefix match the decision was made on; the truth is the engine's own
  `usage.prompt_tokens_details.cached_tokens` for that same request, which the harness asks for on
  every request because it is the only per-request account there is —
  `vllm:request_prefill_kv_computed_tokens` is the same quantity as a histogram and carries no
  request id to join on. It bins the gap by policy, by working set ratio and by time since the
  session was last served, and never nets the two directions: believing in blocks a replica has
  evicted misroutes the request, where forgetting blocks it still holds only forfeits a match. Like
  `cmd/compare` it reads only, so the measurement rebuilds from a checkout. It also writes the
  reading `cmd/calibrate -divergence` folds in, which scales the index's node cap by the share of
  its belief the engines turned out to be honouring
  ([ADR-0008](docs/adr/0008-the-node-cap-is-calibrated-against-measured-divergence.md)).
- **`cmd/preflight`** — refuses to bring the fleet up while any GPU already holds memory. The
  same probe backs the per-cell contamination check: one preflight, two jobs.
- **`cmd/fakereplica`** — a programmable stand-in for a replica with configurable TTFT and
  inter-token latency, so everything above the replica HTTP boundary can be exercised without a
  GPU. `test/contract` holds it to the real engine's behaviour.
- **`ops/fleet.sh`** — preflight, then all six replicas one at a time. Bring-up is sequential on
  purpose: `/home` is a network filesystem and six simultaneous 5.4 GB model loads would put an
  NFS thundering herd inside the first cell's timings.
- **`ops/replica.sh`** — starts one vLLM replica as a bare process on the pinned engine version
  with the AWQ backend forced, refusing to start if either has drifted. `ops/fleet.sh` holds no
  engine settings of its own; a second copy of them is how two cells end up running different
  kernels.

## Run it without a GPU

```sh
make run-fake      # one fake replica on :8000
make run-router    # router on :8080, round-robin, RECORDS=path to keep rows

curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"m","messages":[{"role":"user","content":"hello"}],"stream":true}'

curl -s http://127.0.0.1:8080/router/stats   # router overhead p50/p99, inflight per replica
```

## Run it against the real fleet

```sh
make linux                                                 # static binaries for the box; it has no Go
make fleet-up                                              # preflight, then six replicas, staggered
make contract CONTRACT_REPLICA="$(ops/fleet.sh replicas)"  # hold the fake to every replica
make characterize                                          # capacity, topology, floor, SLO, symmetry
make contention                                            # all six at once, compared by NUMA node
export SLO_FROM=runs/characterization    # the SLO comes from the record, not from typing

# Round-robin first, against a fleet that has served nothing but its own warm-up.
make run-router POLICY=round_robin REPLICAS="$(ops/fleet.sh replicas)" &
make bench   POLICY=round_robin BENCH_ARGS="-cell-duration 60s -repetitions 3"
make goodput POLICY=round_robin GOODPUT_ARGS="-cell-duration 60s -repetitions 3"
kill %1

make fleet-down && make fleet-up        # NOT optional — see below

make run-router POLICY=least_outstanding REPLICAS="$(ops/fleet.sh replicas)" &
make bench   POLICY=least_outstanding BENCH_ARGS="-cell-duration 60s -repetitions 3"
make goodput POLICY=least_outstanding GOODPUT_ARGS="-cell-duration 60s -repetitions 3"
kill %1

make fleet-down && make fleet-up

make run-router POLICY=session_affinity REPLICAS="$(ops/fleet.sh replicas)" &
make bench   POLICY=session_affinity BENCH_ARGS="-cell-duration 60s -repetitions 5"
make goodput POLICY=session_affinity GOODPUT_ARGS="-cell-duration 60s -repetitions 5"
kill %1

# The prefix index's bounds are measured, not chosen, so this runs against a fleet
# that has been under load — the histogram it reads is empty until blocks have
# actually been evicted. Never turn --kv-cache-metrics on now to make it work: that
# changes the engine configuration every cell shares and invalidates all of them.
make calibrate                          # -> runs/prefix-calibration.json

make fleet-down && make fleet-up

make run-router POLICY=prefix_affinity REPLICAS="$(ops/fleet.sh replicas)" &
make bench   POLICY=prefix_affinity BENCH_ARGS="-cell-duration 60s -repetitions 3"
make goodput POLICY=prefix_affinity GOODPUT_ARGS="-cell-duration 60s -repetitions 3"
kill %1

make compare                                               # every policy, both axes, one table
make divergence                                            # how wrong the index was, and by how much

# The node cap is a model of the fleet until something measures how much of that
# model the engines honour. `make divergence` writes that reading; `make calibrate`
# picks it up and resizes the index against it, so prefix affinity is swept again
# on a cap that was checked rather than only derived (ADR-0008).
make calibrate                          # now folds in runs/divergence.json
make fleet-down && make fleet-up
make run-router POLICY=prefix_affinity REPLICAS="$(ops/fleet.sh replicas)" &
make bench   POLICY=prefix_affinity RUN_DIR=runs/concurrency-calibrated
kill %1
```

`make characterize` comes before `make bench` and not after it, because the SLO the sweep is judged
against is derived from what `characterize` measures. `SLO_FROM` then points the sweep at that
record, so the threshold applied is read from the derivation rather than retyped off a table — a
mistyped threshold produces a goodput figure that is internally consistent and silently wrong.
`bench` refuses the record's SLO if the *floor* it came from cannot be built on, and warns rather
than refuses about everything else the record flags, including a symmetry verdict — which is worth
reading, because a policy difference measured on a fleet whose replicas are not interchangeable
could be the host rather than the policy.

`make bench` sweeps concurrency closed-loop; `make goodput` offers a ladder of arrival rates
open-loop and is where the headline goodput number comes from. They land in separate directories
because they are different drivers measuring different things — a closed-loop tail is optimistic by
construction — and both write tables that name the driver behind them.

**Each policy needs its own router, and the same `RUN_DIR` takes them all.** A router runs one
policy, chosen at startup, so a two-policy comparison is two routers in turn. Their cells can share
a directory because a cell id carries its policy, and `make compare` reads them back into one table.
Two policies at the same load point send identical bytes, which is what makes them comparable; that
falls out of the workload slice being keyed on the load axis and the repetition and deliberately not
on the policy.

⚠️ **Bring the fleet down between the two policy passes.** That identical-bytes property is exactly
why: the replicas run with prefix caching on, so the second pass would re-send prompts the first
pass had already prefilled and read them back out of cache. [ADR-0004](docs/adr/0004-every-measurement-sends-unseen-bytes.md)
measured what that is worth — **TTFT p50 of ~325 ms for unseen prompts against ~46 ms for the same
prompts sent again** — so leaving the fleet up would hand whichever policy ran second a sevenfold
head start on the primary metric, and nothing in the resulting latency would say so. A comparison
run that way is the invalid outcome §0 warns about, not a result.

**The SLO stays fixed across both passes, and is not re-derived after the restart.** A comparison
needs one yardstick, so `SLO_FROM` points both passes at the one characterization; `make compare`
refuses cells judged against two different SLOs precisely so this cannot happen quietly. Capacity
has moved between bring-ups of identical configuration before, so characterizing the second bring-up
into its own directory is a worthwhile *check* — if that floor has moved materially, the fleet is
not stable enough for the comparison and that is the finding, rather than a reason to re-derive the
threshold halfway through.

`ops/versions.env` is the single source of truth for everything held constant between cells: the
engine version, the model, the forced quantization backend, the prefix-caching and chunked-prefill
settings, the GPU-dirty threshold and the startup stagger. Nothing downstream keeps a second copy —
`cmd/bench` and `cmd/preflight` have no default for the model, the GPU count or the dirty
threshold, and refuse to run without them; `make bench` and `ops/fleet.sh` read each one out with
`ops/fleet.sh env <NAME>`.

The sweep resumes. Interrupting it leaves the finished cells behind and re-running the same command
loads them rather than recomputing them — but "cached" never means "stuck": the cell that was in
flight when you interrupted is not cached and is re-run, a cell that saw a foreign process is moved
to `discarded/` and re-run, and deriving the SLO later resummarises the cached cells from their own
rows instead of costing another hour of GPU time. See
[ADR-0002](docs/adr/0002-jsonl-during-parquet-after.md).

**The SLO was unset until it had been measured, and now it is measured.** `-slo-ttft` and `-slo-itl`
still default to zero, and a cell run without them records that no SLO was applied rather than
reporting a goodput that was never checked against anything. The derived thresholds are **990 ms and
24 ms**, three times the measured floor. Prefer `-slo-from <characterization>` over passing them:
the two are the same numbers only for as long as nobody mistypes one, and a cell that ran before
they existed is resummarised from its own rows rather than re-run.

## Belief divergence: how wrong the index is, measured

The router models state it does not own. The replicas evict on their own schedule and nothing tells
the router when they do, so its prefix index is a belief that decays — and how fast it decays is a
result this project publishes rather than an error it tidies away. idea.md §1 records that nobody
publishes it at any scale: the sticky-versus-cache-aware ablation has been done at datacentre scale
three times over, and the accuracy of the approximate index those routers decide on has not been.
That is why the result stands whichever policy wins.

Every request carries both sides on one row. The prediction is the prefix match the router made the
decision on, read back off its response header. The truth is the engine's own
`usage.prompt_tokens_details.cached_tokens` for that same request — the harness asks for it with
`stream_options: {"include_usage": true}`, which is the only per-request account there is.
`vllm:request_prefill_kv_computed_tokens` is the same quantity and is a histogram, so it carries no
request id and cannot be joined to the prediction it would check; the report prints it against the
counters as a window-level cross-check instead.

```sh
make divergence DIVERGENCE_DIRS="runs/concurrency runs/goodput"
```

It reads only. Like `make compare`, every figure comes from the rows the sweeps wrote, so the
measurement rebuilds from a checkout with no fleet running and no GPU present.

The report bins the gap three ways and never nets the two directions:

- **By policy.** A policy that consults no index predicts zero on every request, which makes its
  row a reading of what the engines held that no router claimed.
- **By working set ratio** — the pressure that drives eviction. A sweep states its WS point only if
  it was given the measured capacity the ratio is against; pass `-kv-capacity` and the ratio is
  derived from the session pool without changing a byte of what the cell sends, so the frozen
  workload's name, and the comparison, are untouched.
- **By time since the session was last served** — how stale the belief was when it was acted on.
  Derived from the rows rather than recorded by the router, so it costs the routing path nothing.

**Over-prediction and under-prediction are different failures and share no column.** Believing in
blocks a replica has evicted pays the full prefill *and* spends the routing decision on a reason
that stopped being true, which is worse than having routed on load. Forgetting blocks a replica
still holds only forfeits a match. A net figure would report a router that does both equally as one
that does neither.

That asymmetry is what sizes the index. `make divergence` also writes the reading `make calibrate`
folds in, and the node cap becomes the fleet model scaled by the share of its belief the engines
turned out to be honouring — bounded above by the fleet model, ignored when the run claimed nothing,
and ignored when the index never filled its cap, because an index that was never capped did not
over-predict because of the cap. [ADR-0008](docs/adr/0008-the-node-cap-is-calibrated-against-measured-divergence.md)
sets that out, amending [ADR-0006](docs/adr/0006-the-prefix-index-is-bounded-by-measurement.md),
which said plainly that it could not make this check itself.

⚠️ **Cells recorded before this are not comparable with cells recorded after.** Asking for usage
adds about forty bytes to every request body, so both workload names now carry `usage=on` and
`make compare` refuses to put the two in one table — which is [ADR-0004](docs/adr/0004-every-measurement-sends-unseen-bytes.md)'s
rule doing its job. The field sorts last in the marshalled JSON, so every prompt's *leading* bytes
are where they were and the prefix structure the index and the engine both key on is unchanged.

## Design notes worth knowing before reading the code

- **SSE fidelity is non-negotiable.** Break streaming and TTFT stops being measurable, which ends
  the project. `TestStreamingBytesAreIdenticalToHittingTheReplicaDirectly` compares the bytes
  from the router against the bytes from the replica.
- **The decision reason is a return value, not a log line.** The mix of affinity, cold and spill
  decisions is a reported result, so a policy returns it alongside the replica.
- **Router overhead is reported separately**, never folded into TTFT, so the router's own cost is
  visible rather than hidden inside the fleet's latency.
- **Inflight is counted, never scraped, and KV utilization is scraped, never counted.** The split is
  deliberate: the router is the sole ingress so it knows exactly what it dispatched, while cache
  occupancy is the one load signal it cannot derive locally. A scraped inflight would read the same
  for every request arriving inside one polling window and send them all to the same replica.
  `TestExactCountsPreventTheHerdAStaleViewWouldCause` runs both counts over one window of arrivals
  and compares where the requests went. The term `queue_depth` is deleted from this project: vLLM's
  `num_requests_waiting` and the router's inflight are different quantities.
- **The records are the system of record.** Prometheus is a sampled TSDB and the wrong shape for
  per-request tail latency across hundreds of cells; every reported figure is recomputable from
  the JSONL rows.
- **One test seam: the replica HTTP boundary.** Nothing else is faked, and the fake is kept honest
  by a contract test that runs the same assertions against a live replica. The one exception is
  `nvidia-smi`, which is faked at the command boundary so contamination handling can be tested on
  a machine with no GPU.
- **Dropped, failed and SLO-violating never share a column.** Under overload a replica rejects in
  milliseconds; fold those into a latency distribution and an overloaded fleet looks *faster*.
  Percentiles are computed over successful responses only, and a cell past the failure threshold
  is flagged with a reason rather than silently averaged in.
- **Warm-up is a duration, and it is checked rather than trusted.** Two different things are cold.
  A replica's first real forward pass is cold once per process, so every replica is warmed
  *directly at its own port* before the first cell — routing warm-up through the router would let
  round-robin at concurrency 1 warm replicas 0–4 and leave replica 5 to serve the first *measured*
  request cold, manufacturing the cold start it exists to prevent. Per-cell transients (connection
  setup, the queue reaching steady depth) are covered by discarding the first `-warmup` **seconds**
  of each cell rather than a count per virtual user: a count costs `count x per-request latency`,
  and latency grows with concurrency, so the saturated cells would forfeit the largest share of
  their window. Warm-up rows are kept, not deleted, which is what makes the last step possible —
  each cell compares TTFT p50 over the first half of its measured window against the second and is
  **flagged if it was still speeding up**, so a warm-up that was too short is caught rather than
  assumed adequate.
- **A cell that was not checked does not claim to be clean.** Cleanliness needs a successful GPU
  sample with nothing foreign in it. "Nothing was found" and "nothing was looked for" must not read
  the same, so a run with GPU sampling off produces cells that say so and are flagged.
- **Our own replicas are not foreign processes, and our leftovers are.** The pid a pid file records
  is vLLM's API server; the process holding the card is its engine child, so ownership is resolved
  by walking the parent chain. A replica from an earlier run that was never brought down is
  correctly foreign — it is ours, but the fleet this cell is measuring did not start it.
- **JSONL during the run, Parquet after** — see [ADR-0002](docs/adr/0002-jsonl-during-parquet-after.md).
  A crash leaves readable rows; the analysis reads columns. The JSONL is the system of record and
  compaction never deletes it.

## Prior art

Cache-aware routing is already shipped by the SGLang router, NVIDIA Dynamo, llm-d, AIBrix and
Mooncake, and OpenAI has productized its simplest form as `prompt_cache_key`. This project does
not claim the mechanism; it measures where the sophisticated version stops beating the simple one.
