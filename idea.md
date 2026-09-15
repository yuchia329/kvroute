# KV-Cache-Aware Inference Router — Project Spec

**Created:** 2026-09-05 · **Revised:** 2026-09-06 (design review + four grilling rounds)
**Owner:** Yuchia Chang
**Target:** measurement complete before Sep 20, 2026; writeup after
**Positioning:** inference serving & performance — *not* kernel engineering, *not* a from-scratch engine

Domain vocabulary is defined in `CONTEXT.md` and used consistently throughout this document.
Where this spec and the glossary disagree, the glossary wins.

---

## 0. Thesis

> Cache-aware routing is convergent across the field, and OpenAI has productized it: requests are
> already routed by a hash of the initial tokens plus load, with `prompt_cache_key` as a
> disambiguator on top. The open question is not whether prefix affinity beats load balancing. It
> is **at what pressure cache-aware routing starts paying for itself on a single consumer-GPU
> host, and what it costs to approximate cache state the router does not own.**

This is a **characterization study, not a hypothesis to defend.** Four policies, one harness, two
pressure axes, on a fixed 6× RTX 3090 host. The deliverable is the map: which policy wins in
which regime, by what mechanism, and where the additional machinery stops earning its complexity.

**Why this framing rather than "affinity wins."** Session-sticky hashing already captures nearly
all the steady-state locality in independent multi-turn chat — same session, same replica, history
stays cached — at a fraction of the complexity. Prefix affinity's remaining advantage is
concentrated in two places, both about *pressure and failure* rather than steady-state locality:

1. **Load imbalance.** Consistent hashing is blind to load. If the hash lands three heavy sessions
   on one replica and none on another, sticky routing has no escape hatch — it keeps feeding the
   hot replica, because that is where those sessions belong. Spill is the escape hatch. **This is
   the mechanism the project exists to measure.**
2. **Replica loss.** Consistent hashing rehashes a fraction of sessions on failure and those lose
   their cache outright; a prefix index degrades gracefully because it tracks blocks rather than a
   fixed assignment.

A previously-listed third case — shared system prompts across sessions — does **not** favour
prefix affinity on inspection. Sticky hashing scatters those sessions evenly and all six replicas
cache the shared prefix independently, which is fine. Concentrating them would be worse.

**There is no losing outcome. There is an invalid one.** If prefix affinity is within noise of
session affinity everywhere, that is still a publishable result.

⚠️ But it is publishable on **narrower grounds than this spec originally claimed.** Published
comparisons against sticky and consistent-hash baselines now exist — Anyscale, CacheRoute and
llm-d all shipped one between February and August 2026 — every one of them at datacentre scale.
A null result here is publishable as *"the crossover sits at working set X on a 6×3090 host, and
here is the belief divergence that explains it,"* **not** as *"nobody has measured this."* That
second sentence would be caught by anyone in the loop who has read the CacheRoute paper. See §1.

But a null is only worth something if the experiment **could have detected a difference**. A flat
result because the workload never pressured the cache is not a finding, it is a broken experiment,
and it is visible as such to anyone who looks. So the validity condition, stated up front:

> **Validity condition.** A null result is only publishable alongside evidence the mechanism was
> exercised: redundant prefill differed measurably between policies, spill actually fired, and
> prefix cache hit rates diverged. If those are flat too, the workload is wrong — fix the
> workload, do not touch the policy, and do not publish. This is the Phase 2 gate (§10), and it
> is the single most important checkpoint in the project.

**What is never acceptable** is tuning until a win appears. The conclusion is whatever the
measurement says.

**Primary artifact:** the README results table and the pressure map (§14). The router, the Grafana
panels and the process supervision are apparatus.

---

## 1. Why this project

### The gap it closes
Every GPU project currently on the resume is **single-host**. Yapper pools 6 GPUs inside one
daemon; VSS serves vLLM on one box. Production inference is a fleet problem: many replicas,
cache locality across them, tail latency under load, replicas dying. That is the gap.

### The unfair advantage it uses
Very few people with GPU-serving projects also have a production distributed-systems record
(Kubernetes, Terraform, Prometheus/Loki/Alertmanager, SLO 95.8% → 99.8%, MTTR 3h → 40min,
lease/heartbeat crash safety). This project sits exactly on that intersection: it is a
**scheduler and load balancer whose currency happens to be GPU memory**.

### Prior art — know this cold before the first interview
KV-aware routing is an **active, well-known production pattern**, not an original idea. The people
interviewing you at NVIDIA and the hyperscalers ship these systems.

| System | What it does |
|---|---|
| **SGLang** | Rust. The shipping gateway (`sgl-model-gateway`) keeps an **approximate radix tree per worker over raw text**: route to the best prefix match above `cache_threshold`, otherwise to the **smallest tree** (most free cache); a separate imbalance test — `(max−min) > abs_threshold` **and** `max > rel_threshold × min` — overrides both with shortest-queue. Its replacement (`experimental/sgl-router`) **removed those three flags**, falls back to power-of-two-choices, and takes its prefix signal from a router-local hash tree *or* an external gRPC KV indexer |
| **NVIDIA Dynamo** | KV-aware router. A `KVPublisher` on each worker emits stored/removed block events; a `KvIndexer` builds a **global prefix tree** from them, while active decode blocks are counted **locally in the router**. Selects the lowest-cost worker under an explicit formula that credits device/host/disk cache overlap against prefill load and potential decode blocks — *"Higher overlap credits favor cache reuse (improving TTFT), while lower credits prioritize even load distribution (improving ITL)"* |
| **llm-d / Gateway API Inference Extension** | Kubernetes endpoint picker with **pluggable scorers**. A `prefix-cache-scorer` consumes per-endpoint match data from one of three producers: `approx-prefix-cache-producer` (the **default** — routing-history-derived belief, 64-token blocks, per-pod LRU) or `precise-prefix-cache-producer` (a real KV-block index fed by **ZMQ KV events** from vLLM or SGLang). llm-d publishes precise-vs-approximate numbers: P90 TTFT 0.54 s vs 31.1 s. **The EPP moved from `kubernetes-sigs/gateway-api-inference-extension` to `llm-d/llm-d-router`** |
| **AIBrix** | Envoy-gateway ext-proc router. Its `prefix-cache` strategy picks the best prefix-matched pod **within a stddev load threshold**, in either a local-hash-table mode or a KV-event-synced distributed-index mode, behind a **central** cluster-wide load-imbalance gate applied ahead of whichever strategy routes |
| **vLLM production-stack** | Reference router + observability. Ships round-robin and **session-ID routing**; prefix-aware routing is still marked **WIP**. Cite it as evidence that *session stickiness is the shipped default in the vLLM ecosystem* |
| **Mooncake (Kimi / Moonshot)** | Published frontier-lab design (arXiv 2407.00079; FAST '25). Global scheduler **Conductor** hashes prompt blocks, computes a per-instance `prefix_len`, estimates `T_queue + T_prefill` from it, and assigns the **shortest predicted TTFT** — with a second branch that, past `kvcache_balancing_threshold`, prices **migrating the KV to a less-loaded instance** instead. Rejects with HTTP 429 when no instance meets the SLO, using the *greater* of prefill and decode pool load, and predicts decode load to damp the anti-phase oscillation naive early rejection induces. The closest published relative of this project's spill rule |
| **OpenAI `prompt_cache_key`** | **Cache-affinity routing, productized.** OpenAI already routes by *"a hash of the initial tokens"* plus machine load; `prompt_cache_key` is combined with that hash to keep a caller's requests on the same cache. The docs are explicit about its limits: *"Keys influence routing; they do not pin requests to a machine or guarantee a cache read hit."* It replaced the deprecated `user` field, which had done the same bucketing job alongside abuse detection (now `safety_identifier`). Cached input is discounted up to 90%; minimum 1,024 tokens (GPT-5.6+) / 2,048 (older); a cached prefix stays reusable 30 minutes after last use, with an opt-in 24 h retention; a single cache machine overflows above ~15 rpm |
| **MiniMax** | MiniMax-M2 (arXiv 2605.26494, §6.2.6) publishes a *"cost-aware request router [that] dynamically balances queuing delay against cache migration costs, maximizing cache locality without overloading individual instances"* over a DFS-backed global KV cache — in their **agent-RL rollout** stack, not stated to be the production API path |
| **DeepSeek** | Publishes the **cache** and the **disaggregation**, not the router: on-disk context caching in 64-token units with strict 0th-token prefix matching (~56% of tokens hit it), and PD-disaggregated EP32/EP144 serving behind three load balancers that equalize **input tokens, request counts, aggregate KVCache usage and expert dispatch** — none of them prefix-locality-aware |
| **Anthropic / Google** | A different axis: **what to cache and for how long**, not where to route. Anthropic's `cache_control` marks up to 4 breakpoints, 5-minute default TTL or 1 hour, writes at 1.25×/2× and reads at 0.1×. Gemini offers implicit caching on by default (2.5+) and explicit `CachedContent` handles with a 1-hour default TTL. Neither documents anything about routing |

**The field is convergent.** Every system above scores replicas by predicted prefix reuse and
penalizes by load and memory pressure. Policy 4 is that same shape. The two real axes of variation
are (a) approximate router-side index versus exact engine-published KV events, and (b) how
aggressively the load term overrides the locality term — which are precisely this project's
`KV_HIGH_WATER` and `LOAD_IMBALANCE_FACTOR`. **Claiming novelty of mechanism would be false and
instantly caught.**

**⚠️ And the ablation is no longer unpublished either — this is the part that changed.** Between
February and August 2026, three sources published cache-aware routing against a consistent-hash or
sticky baseline:

- **Anyscale / Ray Serve LLM** benchmarked `ConsistentHashRouter` against `KVAwareRouter` and a
  cache-only variant on one harness, and found the load-aware router won on p99 **despite a lower
  prefix cache hit rate**.
- **CacheRoute** used sticky consistent hashing and consistent-hashing-with-bounded-loads as two of
  five baselines, and published an envelope in which *"affinity can reduce capacity when the
  recoverable prefix work is small"* — 0.50–0.67× capacity on one workload.
- **llm-d** published a numeric saturation threshold (τ = 286,720 tokens ≈ 14 s of queued prefill)
  at which it stops honouring stickiness.

So "nobody publishes where the sophisticated thing stops paying" is **no longer true**, and any
README or interview answer that says it will be caught. What is *still* true is narrower and worth
saying precisely:

> Every published version of this comparison runs at datacentre scale — 8–60 H100/H200-class GPUs,
> 32B–70B models, multi-node. None runs it on a single host with six consumer cards and a 4-bit 8B
> model, where aggregate KV is ~14 GiB per replica and a working set can be pushed past fleet
> capacity deliberately. None parameterizes by **working-set ratio over measured aggregate KV**,
> and none separates **memory pressure from load imbalance as independent axes**. And none of them
> measures **belief divergence** — the gap between the router's model of cache residency and the
> engine's actual computed prefill tokens for the same request.

**Required README section and interview answer: how this differs.** Not novelty of mechanism, and
no longer novelty of the comparison either —
(a) the comparison **at single-host, consumer-GPU scale**, where the published results do not
reach and where the crossover sits at a different place;
(b) a **two-axis pressure map** separating working-set ratio from Zipf skew, which no published
version of this comparison does;
(c) a measurement of **belief divergence** between the router's model of cache residency and the
engine's actual state (§4.3), which nobody publishes at any scale; and
(d) if the stretch goal lands, a **replication** of llm-d's approximate-vs-precise result on this
hardware class — framed as a replication, since llm-d already published P90 TTFT 0.54 s vs 31.1 s
for precise vs approximate.

*"I reimplemented the pattern these systems use, then measured it in the regime they don't cover,
and I can tell you exactly where my router's belief about the cache was wrong"* is a strong
position. *"Nobody has published this comparison"* is now a losing one — someone in the loop will
have read the CacheRoute paper.

⚠️ **Verified against primary sources on 2026-09-06** — see `docs/research/prior-art-routing.md`
for per-claim verdicts and URLs. This section carries a **permanent** re-verify warning: three of the
claims above changed in the four weeks before it was written.

### What this is explicitly NOT
- **Not** a from-scratch inference engine (see CoreLLM by classmate: Triton paged-attention
  kernels, continuous batching, radix prefix cache, TP over NCCL/ZMQ, ~153 commits, 3–4 months).
  Rebuilding that = finishing in January, second-mover, strictly worse. Do not.
- **Not** a kernel-optimization project. **No hand-written kernels at all** — profiling only (§9).
- **Not** a demo. The deliverable is a **measured comparison**, not a running service.

---

## 2. Environment

All values below are **measured on the box**, not assumed. Host: `nlp-gpu-01.be.ucsc.edu`.

| Item | Value |
|---|---|
| Host | Ubuntu 24.04.4, kernel 6.8.0-nvidia-lowlatency, driver **595.84** |
| CPU | 2× Xeon Gold 5220R, 96 threads, **2 NUMA nodes**, 1 TB RAM |
| GPU | 6× RTX 3090, 24 GB, Ampere **SM86** (capability 8.6) |
| NVLink | **none** |
| PCIe | **gen 3 x16 max** (reads gen 1 at idle under power management — measure under load) |
| Topology | GPUs 0–3 on NUMA 0, GPUs 4–5 on NUMA 1. Pairs (0,1)(2,3)(4,5) `PIX`; across pairs `NODE`; **across sockets `SYS`** |
| FP8 | **not available** on Ampere. AWQ/INT8 only. |
| Serving | 6 × single-GPU vLLM replicas, ports 8000–8005 — one per card, no tensor parallelism |
| vLLM | **0.28.0**, project-dedicated venv via `uv` (0.11.21). torch 2.13.0+cu130, arch list includes `sm_86` |
| Model | `hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, from the HF cache (5.4 GB, complete) |
| AWQ kernel | **force both switches.** 0.28.0 auto-selects in two independent places: `--quantization awq_marlin` pins the config class, and `--linear-backend marlin` pins the GEMM kernel, which otherwise defaults to `auto`. Pinning one leaves the other free. Verified 2026-09-06; see [ADR-0001](docs/adr/0001-engine-pin-and-forced-kernel-selection.md) |
| Sampler | **`VLLM_USE_FLASHINFER_SAMPLER=0`.** The FlashInfer sampler JIT-compiles a CUDA kernel at startup, which needs `nvcc` on `PATH` and puts a compile step plus a JIT cache inside every replica launch |
| KV residency metrics | **`--kv-cache-metrics`**, off by default. Without it the `kv_block_*` histograms below are absent entirely, not zero |
| Router | Go, cross-compiled `GOOS=linux GOARCH=amd64` on the Mac, scp'd |
| Observability | Prometheus on the box at **127.0.0.1:19091** (9090 is another user's, and 9091 had been taken by a third by 2026-09-10); Grafana on the Mac via `ssh -L 19091:127.0.0.1:19091 nlp`. `ops/prometheus.sh` and `ops/dashboards.sh` |
| Orchestration | **Bare processes.** No Docker, Podman or Apptainer on the box, and no permission to install |

⚠️ **Do not use `~/Meta-Llama-3.1-8B-Instruct-AWQ-INT4`** — those safetensors are 135-byte git-lfs
pointer stubs. Use the HF cache snapshot.

⚠️ `/home` is a **network filesystem**. Six replicas loading 5.4 GB simultaneously will thrash it.
Stagger replica startup or pre-warm the page cache, or the first cell's timings include an NFS
thundering herd.

### Capacity math (do this by hand, it comes up in interviews)
Confirmed against the model's `config.json`: 32 layers, 8 KV heads (GQA), 32 attention heads,
hidden 4096 → head_dim 128, fp16 KV.

```
per layer per token = 2 (K,V) × 8 heads × 128 dim × 2 bytes = 4,096 B
per token           = 4,096 × 32 layers                     = 131,072 B = 128 KiB
```

AWQ 4-bit weights ≈ 5.5 GB. At `--gpu-memory-utilization 0.9` on 24 GB → 21.6 GB, minus weights
and ~1.5–2 GB of activations and CUDA graphs ≈ **14 GiB KV**:

```
14 GiB / 128 KiB  = ~114,700 tokens per replica
× 6 replicas      = ~688,000 tokens fleet-wide
÷ 2k per session  = ~344 resident sessions
```

✅ **Measured 2026-09-06 on all six replicas: 7,872 `num_gpu_blocks` × 16 = 125,952 tokens each,
755,712 fleet-wide** — read off every replica's own `vllm:cache_config_info`, not extrapolated from
one, and identical on all six. That is **9.8% above** the ~114,700 estimated above, and the gap is
entirely in the one soft input: the estimate assumed ~14 GiB was left for KV after weights,
activations and CUDA graphs, and the engine actually left 15.375 GiB. The per-token arithmetic is
exact. See [the characterization](docs/measurements/2026-09-07-characterization/).

A single replica reported **119,408 tokens** at first contact (ADR-0001) and every replica has
reported 125,952 on every bring-up since. That gap is the `torch.compile` cache, and it reproduces
to the token: starting one replica with `VLLM_CACHE_ROOT` pointed at an empty directory — same card,
same settings — gives **119,408 tokens with 19.2 s of compilation**, against 125,952 with 0.26 s on
the warm cache. vLLM sizes the KV cache from what is free after its profiling pass, and on a cold
cache `torch.compile` is still holding ~0.8 GiB while that pass runs.

⚠️ **So KV capacity is not a property of the engine settings alone**, and a constant for it would be
wrong on the first bring-up after any change that invalidates the compile cache. Read it off every
replica at runtime, every run.

⚠️ **The estimate is the estimate; `num_gpu_blocks` is the truth**, and it is read at runtime rather
than off the startup log: the log says it once and then it is gone, while the engine publishes it
for as long as it is up. Every working set ratio in §6 scales off the measured number.

---

## 3. What it looks like when running

`make up` → 6 vLLM replicas + router + Prometheus, all as supervised bare processes with PID
files. Preflight **refuses to start** unless all six GPUs are below a memory threshold (§13).

### Router
OpenAI-compatible `POST /v1/chat/completions`, **SSE streaming preserved**.
Streaming is non-negotiable — break it and TTFT becomes unmeasurable, which kills the project.

Routing decisions stream to the log:

```
req 8f2a  prefix_match=1,847B  → replica-3 (kv 62%, inflight 2)  [AFFINITY]
req 8f2b  prefix_match=0B      → replica-0 (kv 11%, inflight 0)  [COLD/LEAST-LOADED]
req 8f2c  prefix_match=2,104B  → replica-1 (kv 91%, inflight 7)  [SPILL: affinity declined, kv pressure]
```

**The third line is the entire project.** All the interesting engineering is in deciding when to
*refuse* prefix affinity.

### Grafana (5 panels)
1. Per-replica KV utilization + inflight
2. Prefix cache hit rate (global and per replica)
3. TTFT p50/p99 live histogram
4. Routing decisions by reason (AFFINITY / COLD / SPILL)
5. **Router overhead** — p50/p99 accept → upstream dispatch

Grafana is for live watching and for the README screenshots. It does not need to run during
sweeps, and it is **not** the system of record (§6).

### `make bench`
Replays workloads across two sweeps against four policies, emitting per-request records.

---

## 4. Architecture

```
                 ┌───────────────┐
   clients  ───► │  Go router    │ ──► replica-0..5  (vLLM 0.28.0, 1 GPU each)
                 │               │        │
                 │ • prefix trie │        └─► /metrics   KV utilization, 250ms–1s
                 │ • exact       │
                 │   inflight    │        (stretch: KV cache event stream, ZMQ)
                 │ • health/eject│
                 └───────────────┘
```

### Components

1. **Proxy layer** — OpenAI-compatible, SSE passthrough, per-request timing capture
   (client-observed TTFT, ITL, total). **Instrument the router's own added latency** as a
   histogram from accept → upstream dispatch; report p50/p99. A sub-millisecond number here is a
   real result and the most under-sold part of the project.

2. **Session identity** — the **session** is identified by the `X-Session-Id` header. When the
   header is absent, fall back to a hash of the first system+user message, which is stable across
   turns and needs no client cooperation. Both paths ship; the fallback also *is* the
   derived-key policy, so it gives the Q2 sensitivity check for free.

   *Interview answer for "where does the header come from?":* real chat products hold a
   conversation ID in their own database; the OpenAI wire format simply doesn't carry it, so a
   gateway in front of a real product is handed one exactly like this.

3. **Prefix index** — chunk the rendered prompt into **fixed byte blocks** (~64 B ≈ 16 tokens),
   hash them, maintain a trie of `block_hash → {replicas believed to hold it}`. Longest-chain
   match yields **prefix match**, reported honestly in bytes with a measured bytes/token ratio.

   **No tokenizer.** Routing needs only self-consistency — the index ranks replicas against each
   other using its own hashes. Mirroring vLLM's block *size* gives comparable granularity;
   hash-level compatibility is not on the critical path and is not worth cgo plus byte-exact chat
   template rendering.

   **Eviction: TTL + a hard node cap under global LRU, with the cap sized to approximate
   aggregate fleet KV capacity.** An unbounded index believes replicas hold prefixes they evicted
   hours ago, which inflates belief divergence and actively routes badly — affinity to a replica
   whose cache no longer has the data is strictly worse than least-loaded. Sizing the cap to model
   the fleet makes it a defensible modelling decision rather than an arbitrary constant, and
   `vllm:kv_block_lifetime_seconds` / `kv_block_idle_before_evict_seconds` let you calibrate it
   empirically instead of guessing — **but only if the replica was started with
   `--kv-cache-metrics`**, which is off by default. Without it those families do not appear at
   all, so a scrape finds nothing rather than zeros. It is enabled in `ops/versions.env` from the
   first cell onward, because turning it on later would change the engine configuration every
   cell is supposed to share.

4. **Belief divergence** — the router models state it does not own. Log **prefix match** against
   `vllm:request_prefill_kv_computed_tokens` for the same request and plot the divergence. Nobody
   in §1 publishes this; it costs one extra field in the request log.

5. **Load state — two sources, deliberately separated.**
   - **Inflight: counted exactly, in-router.** The router is the sole ingress, so it knows
     precisely what it dispatched and what has not returned.
   - **KV utilization: scraped** from `/metrics`. The only load signal the router cannot derive
     locally.

   ⚠️ **Why the split — herding.** If inflight came from a 250 ms–1 s scrape, every request
   arriving inside one polling window would see the same least-loaded replica and stampede it.
   That is the classic stale-load-balancer failure, and it would silently corrupt *both* baseline
   policies, making policy 4 look good for the wrong reason. Exact local counters eliminate it.

   **The term `queue_depth` is deleted from this project.** vLLM's `num_requests_waiting` and the
   router's inflight are different quantities; conflating them is what the herding argument
   exists to prevent.

6. **Metric names — read off a live vLLM 0.28.0 replica on 2026-09-06**, not assumed. An earlier
   draft of this table dropped the `_total` suffix on every counter and was wrong; the names below
   are what the engine actually serves. `internal/vllmmetrics` is the executable copy, and
   `test/contract` is what re-checks it against a replica.

   | Signal | Metric |
   |---|---|
   | KV utilization | `vllm:kv_cache_usage_perc` ⚠️ *not* `gpu_cache_usage_perc` |
   | Running / waiting | `vllm:num_requests_running`, `vllm:num_requests_waiting` |
   | Prefix cache | `vllm:prefix_cache_hits_total`, `vllm:prefix_cache_queries_total` |
   | Redundant prefill | `vllm:prompt_tokens_total` − `vllm:prompt_tokens_cached_total` ⚠️ `prompt_tokens_recomputed` was **removed** in 0.28.0 |
   | Belief ground truth | `vllm:request_prefill_kv_computed_tokens` (histogram) |
   | Cache residency | `vllm:kv_block_lifetime_seconds`, `kv_block_idle_before_evict_seconds`, `kv_block_reuse_gap_seconds` — **require `--kv-cache-metrics`** |
   | Engine preemption | `vllm:num_preemptions_total` — vLLM's own, **never call this spill** |
   | Server-side latency | `vllm:time_to_first_token_seconds`, `vllm:inter_token_latency_seconds`, `vllm:e2e_request_latency_seconds` |

   ⚠️ **Counters carry `_total`; gauges and histograms do not.** The Prometheus client appends it
   on exposition. This is the single most likely thing to be wrong again after a version bump.

   The Phase 1 gate asserts every one of these exists on a live replica, so a version drift fails
   loudly instead of silently producing zeros. It has been run: it caught the `_total` error and
   the missing `--kv-cache-metrics` flag on first contact.

7. **Health & ejection** — active health checks + passive failure tracking, eject, drain, reroute,
   re-admit on recovery.

---

## 5. The routing policies (this is the experiment)

All policies run against an **identical vLLM configuration**. Only the router varies. Hold
constant: prefix caching enabled, `--gpu-memory-utilization`, chunked prefill setting,
`--max-num-seqs`, CUDA graph settings, model, both quantization switches (`--quantization
awq_marlin` **and** `--linear-backend marlin`), `VLLM_USE_FLASHINFER_SAMPLER=0`, and
`--kv-cache-metrics`.

All of these live in `ops/versions.env`, which is the single source of truth; this list is a
description of that file, not a second copy of it.

| # | Policy | Represents |
|---|---|---|
| 1 | **Round-robin** | the naive baseline |
| 2 | **Least-outstanding** (by inflight) | what nginx / a k8s Service actually gives you |
| 3 | **Consistent-hash session affinity** | **the baseline that matters** — sticky sessions, free in any LB |
| 4 | **Prefix-affinity + KV-pressure spill** | yours |
| 5 | **KV-event-driven exact residency** | *stretch* — what the approximation costs |

### Policy 3 is not optional
Sticky sessions capture most multi-turn cache locality with zero prefix tracking, and every load
balancer ships them — including vLLM's own production-stack, whose prefix-aware routing is still
marked WIP while session-ID routing ships today (§1). **If the trie only beats round-robin, you
built a radix tree to beat a straw man.** Because the session ID arrives as a header, policy 3
gets a perfect oracle, which makes it *stronger* than it would be in production; separating from
it therefore counts for more.

⚠️ **Do not describe policy 3 as "what OpenAI shipped."** Verified: OpenAI already routes by a
hash of the initial tokens *plus machine load*, with `prompt_cache_key` combined into that hash as
a disambiguator. Their production baseline therefore sits closer to **policy 4** than to policy 3,
and their docs are explicit that keys *"influence routing; they do not pin requests to a machine."*
The accurate line is that session stickiness is the shipped default in the **vLLM ecosystem**, and
that frontier providers run something nearer prefix-hash routing with a load term.

Where policy 4 should separate from policy 3, and what the workload must therefore contain:
- **load imbalance under skew** — consistent hashing has no escape hatch when the hash lands hot
  sessions together. **This is the primary mechanism** (§0), and it is why skew is a full sweep
  axis rather than a side experiment
- **rebalancing after replica loss** — consistent hashing rehashes and wrecks its cache; a prefix
  index degrades gracefully (measured in §7)
- **branched / regenerated** conversations sharing a common ancestor under a new session ID
- **RAG-style shared context blocks** reused across sessions, but only where the shared content
  leads the prompt, since prefix caching is prefix-only and not arbitrary-substring

**Struck from this list:** shared system prompts across otherwise-unrelated sessions. Sticky
hashing scatters those evenly and every replica caches the shared prefix independently, which is
fine; concentrating them would be worse. It does not favour policy 4.

**Two published findings worth predicting against** (§1): Anyscale found a load-aware router beat
consistent hashing on p99 *despite a lower prefix cache hit rate*, and CacheRoute found sticky
routing achieved the highest hit rate and the lowest capacity. Both say the load term dominates
the locality term. If this project's pressure map disagrees, that disagreement is the finding and
needs explaining, not smoothing.

### Policy 4 rule
```
candidates = replicas sorted by prefix_match desc
best = candidates[0]
if best.kv_util > KV_HIGH_WATER:                              → spill to least-loaded
elif best.inflight > LOAD_IMBALANCE_FACTOR × min(inflight):   → spill to least-loaded
else:                                                          → route to best  [AFFINITY]
```

Two tunables: `KV_HIGH_WATER`, `LOAD_IMBALANCE_FACTOR`. **Sweep them** — that is a second results
table, and where the affinity-vs-balance tradeoff becomes a curve rather than an assertion.

### Policy 5 (stretch)
vLLM 0.28.0 ships `vllm/distributed/kv_events.py` with `BlockStored`, `BlockRemoved`,
`AllBlocksCleared` and `EventPublisher` — the exact mechanism Dynamo and llm-d use. Policy 5
consumes that stream for exact residency instead of an approximate trie. Running 4 and 5
head-to-head is a **replication** of llm-d's precise-versus-approximate comparison (P90 TTFT
0.54 s vs 31.1 s, §1) at a scale it does not cover: what does the approximation cost on one host
of consumer cards, where aggregate KV is far smaller and eviction far more frequent? Claiming the
question is unpublished would be caught (#24, ADR-0010). Build it only after policy 4 is measured; it must not delay the
clean-window sweeps.

---

## 6. Benchmark methodology

### The system of record is the harness, not Prometheus
Prometheus is a sampled TSDB at 1–15 s resolution — the wrong shape for per-request TTFT p99
across ~220 cells. **The harness writes one row per request**: cell id, policy, session, replica
chosen, decision reason, TTFT, ITL summary, prefix match, actual prefix cache hit,
`request_prefill_kv_computed_tokens`, and outcome (success / SLO violation / failed / dropped).
Written as **JSONL during the run** — a crashed sweep still leaves readable partial data — and
**compacted to Parquet after**. Plots come from those files. Prometheus is for live watching only.

### Workload — two-part, primary one synthetic on purpose

**Primary: parameterized multi-turn generator.** Seeded and deterministic. Knobs for
`n_sessions`, `turns_per_session`, `prompt_len`, `output_len`, **working set ratio**, Zipf skew,
and shared-system-prompt / shared-context fraction.

Why synthetic leads: the interesting result is a *crossover curve*, and a fixed trace gives you
one point on it. The generator is what lets you say "affinity wins above WS X and below WS Y, and
here is why."

**Secondary: ShareGPT multi-turn trace, as a realism check.** One point, confirming the synthetic
result holds on real conversation shapes.

⚠️ **Do not commit ShareGPT to a public repo** — scraped conversation data, unclear licensing,
PII and NSFW content. Commit a **fetch script plus a corpus hash**.

### Sweeps — one scaling axis, two pressure axes

**Axis 1 — concurrency** (closed-loop): 1, 4, 8, 16, 32, 64, 128, 256. A *scaling* axis: how does
each policy's latency degrade as offered load rises.

**The pressure grid** is the headline, and it is two-dimensional because the two pressures are
physically different things and act on different parts of the policy:

**Axis 2 — working set ratio**: **WS ∈ {0.25, 1, 3, 8}** — offered session tokens over measured
aggregate KV. Against the **measured 755,712-token** fleet capacity (§2) that is **92 / 369 / 1,107
/ 2,952** sessions of 2k. The 86 / 344 / 1,030 / 2,750 this said before was computed off the
*estimate*, not off a measurement. WS creates **memory pressure**, which is what drives eviction and
fires the `KV_HIGH_WATER` branch of the spill rule.

**Axis 3 — skew**: **Zipf α ∈ {0.0, 1.0, 1.4}**, from near-uniform to heavily concentrated. Skew
creates **load imbalance**, which is what fires the `LOAD_IMBALANCE_FACTOR` branch.

**Why skew is promoted from a side experiment to a full axis.** Session-sticky hashing is blind to
load: if the hash lands several hot sessions on one replica, it has no escape hatch. That is the
single clearest place prefix affinity with spill can beat it (§0), and it is a *load* phenomenon,
not a memory one. Holding skew fixed would have swept the axis that pressures memory while leaving
the axis that pressures balance untested — measuring everything except the mechanism most likely
to produce the result.

**Why both axes are mandatory:** spill only fires under pressure. At concurrency 256 with a small,
uniform working set you may never trip either threshold, in which case policy 4 collapses into
policy 3 and you get a flat, uninterpretable result after burning a week of GPU time. See the
validity condition in §0.

**Write this prediction down before running.** Not as a hypothesis to defend — as a check on the
harness. Policy 4 should be indistinguishable from policy 3 at low WS and low skew, separate as
either pressure rises, and converge again at extreme WS where prefix caching is futile for
everyone. If nothing separates anywhere, suspect the workload before the policy.

**Do not run the full cross-product:**

| Sweep | Cells | Est. |
|---|---|---|
| Concurrency at nominal pressure | 4 policies × 8 conc × 3 reps = 96 | ~11 h |
| **Pressure grid** (1 fixed concurrency) | 4 × 4 WS × 3 skew × 3 reps = 144 | ~17 h |
| Tunable sweep (policy 4 only) | 3×3 grid × 3 reps × 1 conc = 27 | ~3 h |

≈ **31 GPU-hours per complete pass**, up from 25 when skew was fixed. **Budget 3–4 passes.**

That increase is affordable only because the pressure grid runs at a **single** concurrency
instead of two. If the clean window tightens, cut a WS point before cutting a skew point — the
skew axis is where the mechanism lives.

⚠️ **The harness must be resumable with per-cell caching on day one.** Not week three.

### Closed-loop and open-loop, and why both
A **closed-loop driver** holds N virtual users, each sending its next request only after the
previous response completes — so when the fleet slows, offered load throttles itself, the system
is never pushed past its knee, and the measured tail is systematically optimistic. That is
**coordinated omission**. An **open-loop driver** fires on a fixed arrival schedule regardless.

Goodput under an SLO is a question about behaviour *at and past saturation*, so:
- **closed-loop** drives the concurrency sweep (concurrency is the input);
- **open-loop** produces the **headline goodput number**.

Label every table with which driver produced it.

### Primary metric: goodput, not throughput
Requests/sec meeting the SLO, evaluated per request.

> Throughput numbers with dead p99s are how people lie about serving systems. Leading with
> goodput is itself a signal.

⚠️ **Calibrate the SLO from measurement.** A 2k-token prefill on 8B is ≈ `2 × 8e9 × 2048` ≈
**33 TFLOP**; a 3090 peaks near 35 TFLOPS fp16, so at realistic MFU that is **~1.5 s for a single
prefill with zero queueing**, and AWQ dequant does not help prefill. A hard-coded `TTFT < 1s`
would zero out goodput everywhere and destroy the experiment.

**Procedure:** measure TTFT and ITL at concurrency 1 in Phase 1, then set thresholds as a stated
multiple of that floor (e.g. `TTFT < 3× the c=1 p50`). Publish the floor beside the SLO so the
threshold is visibly derived, not chosen to flatter the result.

### Also record
- TTFT p50 / p95 / p99, ITL p50 / p95
- **Redundant prefill tokens eliminated** — a headline metric alongside goodput
- Prefix cache hit rate per policy
- **Router overhead p50 / p99**
- **Belief divergence** — prefix match vs `request_prefill_kv_computed_tokens`
- Routing decision mix
- `vllm:num_preemptions`, to show engine preemption is distinct from router spill

**Why redundant prefill is promoted to a headline:** it measures the *mechanism*, not just the
consequence. If goodput improves you can show exactly which physical work stopped happening. If
goodput doesn't improve while redundant prefill drops sharply, you have found something more
interesting — that prefill wasn't the bottleneck in that regime — which beats a flat result you
cannot explain.

**Cost per million tokens is dropped.** On your own hardware it is GPU-hours × an invented rate,
and anyone who buys GPU capacity will ask which rate. Reinstate it only with a cited provider and
published price, clearly labelled as a comparable.

### Outcome accounting
`CONTEXT.md` separates **dropped**, **failed** and **SLO violation**, and the harness must use
that separation. If a replica starts fast-failing under load those errors return in
milliseconds — fold them into latency percentiles and **an overloaded fleet looks faster**.

- Three separate columns; percentiles computed **over successful responses only**.
- A cell exceeding ~1% failures is **flagged, not silently averaged in**.

### Contamination — the box is shared
Every cell carries its own cleanliness evidence as first-class fields: `foreign_procs_seen`,
`max_foreign_gpu_mem_mb`, `clean`. Sample `nvidia-smi` process list and per-GPU utilization every
few seconds for the cell's duration. **Any cell with a foreign process on any of the six cards is
discarded and re-run.** On a shared box that field is what makes the numbers defensible.

### Rigor
- Warm up, discard the first N requests
- ≥3 repetitions per cell; p99 is noisy at low sample counts
- Report variance, not just means
- **Separate transport from inference**: difference client-observed TTFT against
  `vllm:time_to_first_token_seconds`. *(Did exactly this in VSS — reuse the technique.)*
- **State the chunked-prefill setting prominently.** It trades TTFT against ITL and partly
  determines which policy looks good. Hold it constant; run one sensitivity check at the opposite
  setting if time allows.

### Expected shape — DO NOT PREDECIDE THE NUMBER
Round-robin scatters consecutive turns of one session across 6 replicas, so each turn re-prefills
history another card already held. Affinity keeps a session home. Session-stickiness gets most of
that for free; the open question is what prefix matching adds on top.

**Measure it, then write it down.** If affinity wins by 12% rather than 60%, that is the number,
and the project is still good. See §0 for the kill condition.

---

## 7. Chaos test

Exercise **both** events — they are different:
- **Drain**: a gracefully removed replica lets in-flight requests finish.
- **`kill -9`**: forces the reroute decision.

Killing a bare process is cleaner and lower-latency than killing a container, so the recovery
curve has less noise than a Compose-based setup would give.

⚠️ **"Zero dropped requests" is not true as stated for streaming.** A request that has already
emitted 200 tokens over SSE cannot be transparently rerouted; a replacement replica starts
generation over and the client sees a discontinuity.

- **Ships, and is the Phase 3 gate:** reroute only requests that have not yet emitted a first
  token; everything else is counted as **dropped**. Report both numbers.
- **Behind a flag if Phase 3 has room:** re-issue mid-stream against a new replica with the
  already-emitted tokens appended as a continuation. Better story, and the continuation request
  *is* a long-shared-prefix request, so it exercises the routing mechanism during failure. Splice
  correctness needs its own test — a subtly corrupted stream that still looks fine is a bad thing
  to have in a portfolio repo.

**Tie back to §5:** run the chaos test under **both policy 3 and policy 4** and compare recovery
curves. Consistent hashing rehashes and wrecks its cache on topology change; a prefix index
degrades gracefully. That converts the chaos test from a nice graph into a second result.

---

## 8. Disaggregated prefill/decode — arithmetic first, build only if it survives

**Do not start by building this, and do not predecide the result.**

The original draft asserted disagg would lose to PCIe bandwidth. That contradicts §6's own rule,
and the prediction is shaky: 2k tokens × 128 KiB = **256 MiB of KV per request**; at PCIe 3.0 x16
(~12–13 GB/s realistic) that is **~20 ms** against a ~1.5 s prefill. Bandwidth is not obviously
the killer. The real costs are more likely halving decode capacity and adding a hop — and on this
box, the `SYS` link between NUMA nodes, which is the one transfer path that genuinely might hurt.

> ⚠️ **Measured, 2026-09-11 (#22): bandwidth is not the killer, and the prediction above was
> optimistic by six times.** Moving a 2,048-token request's KV costs **36.7 ms** against a measured
> **490.7 ms** prefill — **7.5%**, not the 1.3% this paragraph estimates. Two measured reasons: no
> pair of cards on this host has peer access at all (the driver reports the chipset unsupported on
> all 30 pairs), so every card-to-card copy is staged through host memory at 7.4–7.8 GB/s rather
> than moving over a 12–13 GB/s direct link; and prefill is three times faster than the ~1.5 s
> assumed here. CUDA's own peer copy gets half that: 3.7 GB/s, because the driver's staging is not
> pipelined. The `SYS` link is **not** the one that hurts — every class lands within 5% of the
> others when a pair copies alone; what halves bandwidth is two cards behind one PCIe switch sending
> at once, since they share its single x16 uplink.
>
> The verdict still falls the way this section leans, on the costs the arithmetic cannot remove
> rather than on bandwidth — and one it can: with prefix caching, a later turn's prefill shrinks
> while the KV to ship does not, which puts the transfer at 30% of the prefill it would replace.
> See [the measurement](docs/measurements/2026-09-11-pcie-arithmetic/).

Standing up a real KV transfer path on 3090s is a multi-week yak shave, and a negative result
from a setup you fought for a week is **indistinguishable from a misconfiguration**.

### Do this instead (one afternoon)
1. Record topology and lane widths (`nvidia-smi topo -m` — already captured in §2).
2. Microbenchmark real P2P and host-bounce bandwidth **under load**, per card pair — `PIX`,
   `NODE` and `SYS` pairs separately, since they will differ substantially.
3. Divide KV bytes per request by measured bandwidth; compare against measured prefill time.
4. **Publish the arithmetic and the measured bandwidth.** That alone is a legitimate result.
5. Only *then* decide whether building it is worth two weeks.

3% of the effort, it cannot produce a wrong conclusion from a broken setup, and it demonstrates
the same instinct — reason about the hardware before adopting the architecture diagram.

---

## 9. Profiling, not kernels

**Do not write a Triton kernel.** A fused RMSNorm does not teach you paged attention and costs a
week.

**Do this instead (one day):** profile vLLM's decode and prefill steps with **Nsight Compute**.
Produce the roofline showing memory-bandwidth-bound decode against compute-bound prefill, on your
hardware, with your model. Same interview payoff, one-seventh the time, and honest to the identity
§12 stakes out.

The ~3 weeks reclaimed from §8 and §9 go to sweep reruns.

---

## 10. Milestones

The GPU box is **shared but idle until Sep 20**. That inverts the original plan: GPU work is
front-loaded into the clean window, and everything that needs no GPU moves after it.

| Phase | Dates | Output |
|---|---|---|
| 1 | Sep 6–13 | 6 replicas up; router with SSE intact; resumable harness; workload generator; **all four policies**; both drivers; baselines measured; topology recorded |
| 2 | Sep 14–19 | **Clean window.** Concurrency sweep; WS sweep; tunable sweep; chaos runs; belief-divergence data |
| 3 | Sep 20–Oct 3 | Zero-GPU work: plots, comparison tables, Grafana panels, README, writeup. Plus policy 5 stretch, PCIe arithmetic (§8), Nsight roofline (§9), ShareGPT realism check |

Applications go out **in parallel, starting now** — see §13.

### Phase gates

**Phase 1 — all must pass before the clean window opens:**
- Router adds **< 1 ms p99** overhead
- SSE stream **byte-identical** to hitting a replica directly
- **Concurrency-1 TTFT and ITL measured, SLO derived from them**
- **Metric-name assertion** against a live replica for every metric in §4.6
- `num_gpu_blocks` read; real KV headroom recorded; WS session counts rescaled
- **Replica symmetry check** (below)
- Preflight refuses to start on dirty GPUs
- Workload generator seeded and deterministic; ShareGPT corpus pinned by hash

**Replica symmetry check.** The six GPUs are identical 3090s, but the *host* is not symmetric:
GPUs 0–3 share NUMA 0's 48 cores (12/GPU) while GPUs 4–5 share NUMA 1's 48 (24/GPU). vLLM's
per-step CPU work — API server, tokenization, detokenization, engine loop — is host-side, so at
concurrency 128–256 the NUMA 0 replicas may be CPU-starved relative to NUMA 1. Round-robin
touches all six while affinity concentrates, so an 8% systematic difference would leak topology
into the policy comparison.

*(PCIe is not the concern here. For co-located inference it carries the model once at load and
then only token-sized transfers. The `SYS` link matters for §8 and essentially nowhere else.)*

**Settle it empirically:** drive each replica individually at concurrency 1 and again at moderate
concurrency with identical load; compare TTFT and ITL. If all six match within noise, no action —
and you have a symmetry check for the README. If not, pin with `numactl --cpunodebind` and cap
every replica to the same core count. If pinning doesn't fix it, **drop to GPUs 0–3 only**: four
symmetric replicas beat six confounded ones.

> ⚠️ **Measured, 2026-09-07: this method cannot detect the asymmetry this host actually has, and
> the escalation ladder above is the wrong ladder.** Both corrections matter more than the NUMA
> hypothesis they replace.
>
> **The method's blind spot.** "Drive each replica individually" is the one configuration in which
> the effect cannot appear. Driven one at a time the six agree to **1.5%**, and two further
> independent runs agree — 2.1% at bring-up, 2.3% in the solo characterization. Driven *all six at
> once*, the spread is **12.7%** and grows with time on load. Whatever is asymmetric here is only
> asymmetric when the whole fleet is busy, which is also the only condition the sweep ever runs in.
> **Drive all six simultaneously, or the check passes vacuously.**
>
> **The cause is thermal, not topological.** GPU 3 is the only card predominantly limited by heat —
> `SwThermal` in 67% of samples against 0–34% for the other five — clocking down to 960 MHz while
> the rest hold 1305 MHz or better, and doing so at a *lower* core temperature (75 °C) than GPU 4
> tolerates without throttling at all (83 °C). Its deficit grows 1.1% → 12.7% → 17.2% over three
> minutes of sustained load. NUMA is ruled out: the effect does not follow the node boundary, and
> pinning every replica to twelve disjoint local threads left it slowest in 39 of 41 five-second
> slices, unchanged from unpinned's 38 of 41. See
> [the measurement](measurements/2026-09-07-gpu3-thermal/).
>
> **So the fallback above is wrong for this box: GPUs 0–3 *includes* the bad card.** A symmetric
> subset here is 0,1,2,4,5 or 0,1,4,5. The better escalation, which this ladder does not contain,
> is a **uniform power cap** — `nvidia-smi -pl` at a wattage every card sustains, which equalises
> the six by construction the way pinning was supposed to. It needs root, which we do not have on
> this host.
>
> **And the asymmetry is time-varying**, which no rung of this ladder anticipates. A cell's result
> depends on the thermal history of what ran before it, so it correlates with sweep order rather
> than cancelling across policies. A throttled cell is as invalid as a contaminated one and must be
> recorded as such — see the follow-up issue.

**Phase 2 gate — the one that saves a wasted week:**
- **Prefix cache hit rate differs measurably between policies.** If not, **the workload is wrong.
  Fix the workload, do not touch the policy.**

**Phase 3 gate:**
- Recovery curves for both policy 3 and policy 4
- **Honest drop accounting** per §7 — no unqualified "zero dropped"

**Demoable at ~40%.** By end of Phase 1 there is a real interview conversation: "here is the
harness, here are three baselines, here is the SLO I derived and why, here is what I measure next."

---

## 11. Resume bullets

House style: metric-led, mechanism stated, past tense.
**Bracketed values are placeholders. Never ship an unmeasured number.**

**Bullet 1 has two variants. Write whichever the measurement supports — never both, never the
first one hedged.**

*If prefix affinity wins somewhere:*
- Raised inference goodput under a measured TTFT/ITL SLO by [X]% on a fixed 6×RTX 3090 host by routing requests to the vLLM replica already holding their session prefix, cutting redundant prefill by [X]M tokens and outperforming consistent-hash session affinity above [X]× KV working set

*If it does not — equally shippable, and rarer:*
- Mapped where KV-cache-aware routing stops paying for itself, showing consistent-hash session affinity within [X]% of a prefix-affinity router across a 4×3 pressure grid on a fixed 6×RTX 3090 host, and attributing the result to [mechanism] via per-request redundant-prefill accounting

- Built the router in Go with exact in-flight accounting and 250 ms KV-utilization scraping, adding [X] ms p99 to the request path while eliminating the stale-load-state herding that corrupts naive least-loaded balancing
- Benchmarked four routing policies across a concurrency sweep to 256 and a two-dimensional pressure grid spanning 0.25×–8× aggregate KV capacity and Zipf skew 0–1.4, separating memory pressure from load imbalance as independent drivers of routing behavior
- Held [X]% of in-flight requests through mid-benchmark replica loss via health-check ejection, connection draining and reroute, recovering to steady-state goodput in [X]s — against [X]s for consistent-hash routing, which rehashes its cache on topology change
- Instrumented routing decisions, per-replica KV headroom and cache hit rate as Prometheus histograms with Grafana panels, making cache-locality behavior observable per request instead of inferred from aggregate latency

*Optional sixth, only if policy 5 lands:* Quantified the cost of approximate cache tracking by running an event-driven exact-residency router against the approximate index, showing [X]% of the achievable gain is captured without engine cooperation

### Changes from the first draft, and why
- **Old bullets 1 and 2 were the same bullet.** Merged; the slot went to the Go router / herding
  bullet, which is the distributed-systems flex a from-scratch-engine author cannot write.
- **"Zero dropped requests" → a scoped percentage** with the mechanism named (§7).
- **Cost per million tokens removed** — an invented rate is a liability.
- **The disagg bullet is gone** until §8's arithmetic exists. It asserted an unmeasured conclusion
  that may not even hold.
- **"Outperforming session affinity" added** — materially stronger than beating round-robin, and
  the claim §5 is built to support.
- **Bullet 1 now has a null variant** (§0). The characterization framing means the project ships a
  bullet either way; the variant that gets written is decided by the data, not by preference. The
  null variant is not a consolation prize — "I found where the sophisticated approach stops
  earning its complexity" is a harder thing to say than "mine was faster," and interviewers who
  build these systems know it.
- **The sweep bullet now names both pressure axes** — separating memory pressure from load
  imbalance is the design decision worth signalling, and it is what makes the null interpretable.

---

## 12. Interview defense

A project you cannot defend line-by-line is worse than no project. A coding agent compresses the
code, not the understanding. Be able to answer, cold:

**On the domain**
- Why is decode memory-bandwidth-bound and prefill compute-bound?
- Compute the KV cache size for this model from its dimensions. (§2)
- Why is your measured KV headroom smaller than that arithmetic? (§2)
- Why does batching help decode far more than prefill?
- What does continuous batching change vs static batching?
- AWQ vs GPTQ vs FP8 — and why FP8 is unavailable on Ampere.
- What breaks first as concurrency rises: KV capacity, compute, or scheduler overhead?

**On the design — expect these first, they are the sharp ones**
- **How is this different from SGLang's cache-aware router, or Dynamo's KV router?** (§1)
- **Why not just sticky sessions?** What does prefix matching buy over consistent hashing, and did
  you measure it? (§5)
- Your trie models cache state you don't own. How wrong does it get, and how do you know? (§4.4)
- Why count inflight locally instead of scraping it? (§4.5)
- Closed-loop vs open-loop — which did you use for the headline number, and why does it matter?
- How did you pick your SLO threshold? (§6 — "derived from the measured c=1 floor," not "1 second
  sounded right")
- Your box is shared. How do you know your numbers aren't a neighbour's noise? (§6)
- Six identical GPUs — but are your replicas actually interchangeable? (§10)
- When does prefix affinity become the *wrong* choice?
- What did you get wrong first, and how did the measurement tell you?

On kernels, the honest line: *"No kernels — I profiled vLLM's with Nsight and can show you the
roofline. My depth is the layer above it — scheduling, cache locality, tail latency across
replicas."* Coherent, and rarer than another kernel person.

---

## 13. Scope guards

**Do not build:** a from-scratch engine, **any custom kernels**, multi-node, a training path, a UI
beyond Grafana, speculative decoding, your own quantization, a KV transfer path before §8's
arithmetic justifies it, a clock abstraction before flakiness demands one.

**Operational hygiene on a shared box:**
- `make up` **refuses to start** unless all six GPUs are below a memory threshold. The same probe
  backs the per-cell contamination check — one preflight, two jobs. It protects you from
  lab-mates *and* from your own leftovers, which have already been the actual problem once.
- Prometheus on **127.0.0.1:19091**; 9090 and 9091 belong to other users, and `ops/prometheus.sh`
  refuses any port someone already holds.
- Stagger replica startup against the NFS home.

**Do not wait for this project to start applying.** A December offer needs loops starting in
October — cycles run 4–8 weeks. There is already enough GPU evidence in `main.tex` to interview
for inference-serving roles today; it is just buried. This project converts Q1 2027 offers and
gives live material for October loops — "here is what I am measuring right now" is a strong
answer.

**Sponsorship filter:** apply the existing E-Verify / sponsorship screen before targeting
GPU-cloud startups; many are too small to sponsor, and some HPC/defense-adjacent infra is
citizenship-gated. NVIDIA, hyperscalers, and larger labs do sponsor.

---

## 14. README structure for the repo

1. One-sentence what-it-is, framed as the question (§0) rather than as a claimed win
2. **The results table, immediately** — 4 policies × goodput / TTFT p99 / cache hit rate / redundant prefill
3. **The pressure map** — the headline figure: goodput delta between policy 3 and policy 4 across the WS × skew grid, showing where cache-aware routing pays and where it does not
4. Two supporting plots: goodput vs concurrency; cache hit rate and redundant prefill by policy
5. Chaos-test recovery graph — policy 3 vs policy 4, with drop accounting
6. Architecture diagram
7. **Prior art and how this differs** (§1) — cite SGLang, NVIDIA Dynamo, llm-d, AIBrix, vLLM production-stack, **Mooncake**, MiniMax-M2, DeepSeek and **OpenAI's `prompt_cache_key`**. Then state plainly that the sticky-vs-cache-aware ablation **has been published** (Anyscale, CacheRoute, llm-d) and that this project's contribution is narrower: the same comparison at single-host consumer-GPU scale, a two-axis pressure map, and belief divergence — which nobody publishes at any scale
8. Reproduce: `make up`, `make bench`
9. Methodology: what was held constant, **how the SLO threshold was derived**, closed-loop vs open-loop, contamination handling, replica symmetry check
10. Belief divergence: prefix match vs actual computed prefill tokens
11. Negative and null results, stated plainly — the PCIe/disagg arithmetic (§8), and "prefix
    affinity did not beat session affinity in regime X" if that is what happened. Under the §0
    framing these are findings in the same voice as the positive ones, not an appendix
