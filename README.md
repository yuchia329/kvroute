# kvroute

A KV-cache-aware inference router fronting six single-GPU vLLM replicas.

The question it exists to answer: **where does cache-aware routing stop paying for itself against
the consistent-hash session-affinity baseline any load balancer already gives you for free?** The deliverable is a
measured comparison of routing policies, not a service. See [`idea.md`](idea.md) for the spec and
[`CONTEXT.md`](CONTEXT.md) for the vocabulary.

> **Status: the thinnest complete path is in place** — a request reaches a replica through the
> router with SSE intact, and lands as a row in the record. The results table, the policies and
> the pressure map are not built yet. This README gets replaced by a results-first one once there
> are results.
>
> **Not yet run against a GPU.** Everything above the replica HTTP boundary is tested and passing
> against the fake. Nothing here has met a real vLLM replica: `ops/replica.sh` has never been
> executed, the pinned engine version and the forced AWQ backend are transcribed from the spec
> rather than confirmed on the box, and `TestLiveReplicaHonoursTheContract` has only ever skipped.
> The metric names in `internal/vllmmetrics` are the spec's claim until that test runs. Expect the
> first contact with the box to fail on one of those and fix it there — that is what the
> assertions are for.

## What runs today

```
client ──► router (:8080) ──► replica (:8000)
              │                   │
              │                   └─ /v1/chat/completions (SSE), /metrics
              └─ round-robin policy, per-request JSONL rows, own-overhead percentiles
```

- **`cmd/router`** — OpenAI-compatible `POST /v1/chat/completions` with SSE passed through
  untouched, one JSONL row per request, and its own accept-to-dispatch cost reported at
  `GET /router/stats`.
- **`cmd/fakereplica`** — a programmable stand-in for a replica with configurable TTFT and
  inter-token latency, so everything above the replica HTTP boundary can be exercised without a
  GPU. `test/contract` holds it to the real engine's behaviour.
- **`ops/replica.sh`** — starts one vLLM replica as a bare process on the pinned engine version
  with the AWQ backend forced, refusing to start if either has drifted.

## Run it without a GPU

```sh
make run-fake      # one fake replica on :8000
make run-router    # router on :8080, round-robin, RECORDS=path to keep rows

curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"m","messages":[{"role":"user","content":"hello"}],"stream":true}'

curl -s http://127.0.0.1:8080/router/stats   # router overhead p50/p99
```

## Run it against a real replica

Unrun so far, in this order:

```sh
make replica-up INDEX=0                                    # pinned vLLM, forced awq_marlin
make contract CONTRACT_REPLICA=http://127.0.0.1:8000       # hold the fake to the engine
make router-linux                                          # static binary for the box
```

`ops/versions.env` is the single source of truth for everything held constant between cells: the
engine version, the model, the forced quantization backend, and the prefix-caching and
chunked-prefill settings.

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
  by a contract test that runs the same assertions against a live replica.

## Prior art

Cache-aware routing is already shipped by the SGLang router, NVIDIA Dynamo, llm-d, AIBrix and
Mooncake, and OpenAI has productized its simplest form as `prompt_cache_key`. This project does
not claim the mechanism; it measures where the sophisticated version stops beating the simple one.
