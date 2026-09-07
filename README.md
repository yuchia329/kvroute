# kvroute

A KV-cache-aware inference router fronting six single-GPU vLLM replicas.

The question it exists to answer: **where does cache-aware routing stop paying for itself against
the consistent-hash session-affinity baseline any load balancer already gives you for free?** The deliverable is a
measured comparison of routing policies, not a service. See [`idea.md`](idea.md) for the spec and
[`CONTEXT.md`](CONTEXT.md) for the vocabulary.

> **Status: the fleet and the harness are in place** — six replicas come up behind a preflight,
> a closed-loop driver sweeps concurrency across them under round-robin, and every cell lands as
> a row in the results table with its own contamination evidence. What is missing is the
> comparison: three of the four policies, both pressure axes, and the multi-turn workload that
> makes cache locality exist at all. This README gets replaced by a results-first one once there
> are results.
>
> **Verified on the box, 2026-09-06.** All six replicas came up under `ops/fleet.sh` behind the
> preflight, a sweep ran across them through the router, and every cell came back `clean`. The
> numbers below are from that run. See
> [ADR-0001](docs/adr/0001-engine-pin-and-forced-kernel-selection.md) for what first contact
> corrected.

## First measurements

Six replicas, `Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, one per RTX 3090, brought up under
`ops/fleet.sh` on 2026-09-06. Not a benchmark — two 20-second cells at concurrency 1 and 8, on a
workload with no shared prefixes, recorded so later figures have a reference point.

| Quantity | Measured | Note |
|---|---|---|
| Router overhead p50 / p99 | **135 µs / 318 µs** | 280 requests. Against the < 1 ms p99 Phase 1 gate — passes, though the max of 1.34 ms includes first-connection setup |
| TTFT p50 / p99 at concurrency 1 | **320 ms / 330 ms** | the hardware floor the SLO gets derived from in #10 |
| Inter-token latency p50 | **7.6 ms** | |
| KV cache capacity | **125,952 tokens per replica**, identical on all six | **755,712 fleet-wide.** See the discrepancy below |
| Model load | 4.7–5.2 s per replica | sequential bring-up, no NFS thundering herd |
| Contamination | 11 samples per cell, 0 foreign processes, `clean: true` | the ownership check works: the process holding each card is *not* the pid in the pid file |

⚠️ **The capacity figure moved and has not been explained.** [ADR-0001](docs/adr/0001-engine-pin-and-forced-kernel-selection.md)
recorded **119,408 tokens** from a single replica during the first bring-up; all six now report
**125,952**, a 5.5% increase, with identical engine settings. Six agreeing replicas is the stronger
measurement, but the gap is unexplained and every working set ratio scales off this number.
Reconciling it against `num_gpu_blocks` is an acceptance criterion of #10 and is not done here.

The rows behind every figure above are kept in
[`docs/measurements/2026-09-06-fleet-bringup/`](docs/measurements/2026-09-06-fleet-bringup/) —
per-request JSONL, the compacted Parquet, and each replica's own startup log. Sweep output is
gitignored because a full pass is hundreds of megabytes; a reference run the README cites is not,
because a figure whose rows have been deleted is an assertion rather than a measurement.

Concurrency 8 barely moved TTFT (328 ms p50) — six replicas are nowhere near saturation at that
load, which is the whole reason the sweep runs to 256.

## What runs today

```
cmd/bench ──► router (:8080) ──► replica-0..5 (:8000..:8005, one GPU each)
    │             │                   │
    │             │                   └─ /v1/chat/completions (SSE), /metrics
    │             └─ round-robin policy, per-request JSONL rows, own-overhead percentiles
    └─ closed-loop driver, per-cell caching, nvidia-smi contamination sampling,
       JSONL during the run → Parquet after

cmd/characterize ─────────────► replica-0..5, one at a time, no router in the path
    └─ fleet KV capacity off every replica, host topology, the latency floor,
       the SLO derived from it, and the replica symmetry verdict
```

- **`cmd/router`** — OpenAI-compatible `POST /v1/chat/completions` with SSE passed through
  untouched, one JSONL row per request, and its own accept-to-dispatch cost reported at
  `GET /router/stats`.
- **`cmd/bench`** — the closed-loop driver and the concurrency sweep. Refuses to start unless every
  replica answers `/health`, warms each one directly, then holds a fixed number of virtual users at
  each of eight levels, counting dropped, failed and SLO-violating requests in three separate
  columns. Samples the GPUs throughout and resumes from cached cells.
- **`cmd/characterize`** — establishes the measured facts every other number scales off, in one
  pass: aggregate KV capacity read off all six replicas' own `num_gpu_blocks`, the host's GPU
  topology and NUMA placement, the hardware latency floor, the SLO derived from that floor as a
  stated multiple, and whether the six replicas are interchangeable. It drives each replica
  directly, because every question it answers is about a replica and the router's policy would
  otherwise be in the answer.
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

curl -s http://127.0.0.1:8080/router/stats   # router overhead p50/p99
```

## Run it against the real fleet

```sh
make linux                                                 # static binaries for the box; it has no Go
make fleet-up                                              # preflight, then six replicas, staggered
make contract CONTRACT_REPLICA="$(ops/fleet.sh replicas)"  # hold the fake to every replica
make characterize                                          # capacity, topology, floor, SLO, symmetry
make run-router REPLICAS="$(ops/fleet.sh replicas)"
make bench BENCH_ARGS="-cell-duration 60s -repetitions 3 -slo-ttft 960ms -slo-itl 24ms"
```

`make characterize` comes before `make bench` and not after it, because the SLO the sweep is judged
against is derived from what `characterize` measures. It prints the two flags to pass on, so the
threshold the sweep applies is the one that was derived rather than one retyped off a table.

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

**The SLO is deliberately unset until it has been measured.** `-slo-ttft` and `-slo-itl` default to
zero, and a cell run without them records that no SLO was applied rather than reporting a goodput
that was never checked against anything — the concurrency-1 cell is what the threshold gets derived
from, so it necessarily runs before one exists.

## Design notes worth knowing before reading the code

- **SSE fidelity is non-negotiable.** Break streaming and TTFT stops being measurable, which ends
  the project. `TestStreamingBytesAreIdenticalToHittingTheReplicaDirectly` compares the bytes
  from the router against the bytes from the replica.
- **The decision reason is a return value, not a log line.** The mix of affinity, cold and spill
  decisions is a reported result, so a policy returns it alongside the replica.
- **Router overhead is reported separately**, never folded into TTFT, so the router's own cost is
  visible rather than hidden inside the fleet's latency.
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
