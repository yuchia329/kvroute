# KV-Cache-Aware Inference Router — Project Spec

**Created:** 2026-09-05 · **Revised:** 2026-09-06 (design review + four grilling rounds)
**Owner:** Yuchia Chang
**Target:** measurement complete before Sep 20, 2026; writeup after
**Positioning:** inference serving & performance — *not* kernel engineering, *not* a from-scratch engine

Domain vocabulary is defined in `CONTEXT.md` and used consistently throughout this document.
Where this spec and the glossary disagree, the glossary wins.

---

## 0. Thesis

> Cache-aware routing is convergent across the field, and OpenAI has productized its simplest
> form as `prompt_cache_key`. The open question is not whether prefix affinity beats load
> balancing. It is **at what pressure cache-aware routing starts paying for itself, and what it
> costs to approximate cache state the router does not own.**

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
session affinity everywhere, that is a publishable result and a rare one: every vendor publishes
wins against round-robin, nobody publishes where the sophisticated thing stops paying against the
baseline a frontier lab actually shipped.

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
KV-aware routing is an **active, well-known production pattern**, not an original idea. The
people interviewing you at NVIDIA and the hyperscalers ship these systems.

| System | What it does |
|---|---|
| **SGLang router** | Rust, cache-aware load balancing over an approximate radix tree per worker |
| **NVIDIA Dynamo** | KV-aware router; consumes KV cache events from workers to track block residency |
| **llm-d / Gateway API Inference Extension** | Kubernetes endpoint-picker with prefix-cache-aware scoring |
| **AIBrix** | AI-native control plane with cache-aware gateway routing |
| **vLLM production-stack** | Reference router + observability for multi-replica vLLM |
| **Mooncake (Kimi / Moonshot)** | Published frontier-lab design. Global scheduler estimates prefix hit length per instance, balances against instance load, and **rejects early** on predicted load — the spill rule generalized into admission control. The closest published relative of this project |
| **OpenAI `prompt_cache_key`** | **Session affinity, productized.** An API parameter whose documented purpose is routing requests sharing a key to the same machine to raise cache hit rates. This is policy 3, shipped by a frontier lab — which is exactly why policy 3 is the baseline that matters and round-robin is not |

⚠️ **Verify these before putting them in the README.** They are written from recall against a
May 2026 knowledge cutoff, in a field that moves monthly. `prompt_cache_key`'s exact semantics and
Mooncake's scheduler details both need checking against primary sources — a wrong prior-art claim
in an interview is worse than no prior-art section. Run `/research` on this before publishing §1.

**The field is convergent.** Every system above scores replicas by predicted prefix reuse and
penalizes by load and memory pressure. Policy 4 is that same shape. The two real axes of variation
are (a) approximate router-side index versus exact engine-published KV events, and (b) how
aggressively the load term overrides the locality term — which are precisely this project's
`KV_HIGH_WATER` and `LOAD_IMBALANCE_FACTOR`. **Claiming novelty of mechanism would be false and
instantly caught.** What is unpublished is the ablation.

**Required README section and interview answer: how this differs.** Not novelty of mechanism —
(a) a **head-to-head policy comparison under one controlled harness**, which none of the above
publish for this hardware class; (b) a **quantified affinity-vs-balance crossover** as a function
of working set ratio; (c) a measurement of **belief divergence** between the router's model of
cache residency and the engine's actual state (§4.3); and (d) if the stretch goal lands, a direct
measurement of **what the approximation costs** versus event-driven exact residency (§5, policy 5).

"I reimplemented the pattern these systems use, then measured the thing they assert" is a strong
position. "I invented cache-aware routing" is a losing one.

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
| AWQ kernel | **force `backend='awq:marlin'`.** 0.28.0's `auto_awq.py` auto-selects Marlin; pin it so it cannot flip between runs |
| Router | Go, cross-compiled `GOOS=linux GOARCH=amd64` on the Mac, scp'd |
| Observability | Prometheus on the box at **:9091** (9090 is another user's); Grafana on the Mac via `ssh -L 9091:localhost:9091 nlp` |
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

⚠️ **That is the estimate; `num_gpu_blocks` is the truth.** Read it off vLLM at startup and
compute `num_gpu_blocks × block_size × 128 KiB`. Every working set ratio in §6 scales off the
measured number. Publish both, and the gap between them — it is a good interview story.

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
   empirically instead of guessing.

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

6. **Metric names — verified against vLLM 0.28.0**, not assumed:

   | Signal | Metric |
   |---|---|
   | KV utilization | `vllm:kv_cache_usage_perc` ⚠️ *not* `gpu_cache_usage_perc` |
   | Running / waiting | `vllm:num_requests_running`, `vllm:num_requests_waiting` |
   | Prefix cache | `vllm:prefix_cache_hits`, `vllm:prefix_cache_queries` |
   | Redundant prefill | `vllm:prompt_tokens` − `vllm:prompt_tokens_cached` ⚠️ `prompt_tokens_recomputed` was **removed** in 0.28.0 |
   | Belief ground truth | `vllm:request_prefill_kv_computed_tokens` |
   | Cache residency | `vllm:kv_block_lifetime_seconds`, `kv_block_idle_before_evict_seconds`, `kv_block_reuse_gap_seconds` |
   | Engine preemption | `vllm:num_preemptions` — vLLM's own, **never call this spill** |
   | Server-side latency | `vllm:time_to_first_token_seconds`, `vllm:inter_token_latency_seconds`, `vllm:e2e_request_latency_seconds` |

   The Phase 1 gate asserts every one of these exists on a live replica, so a version drift fails
   loudly instead of silently producing zeros.

7. **Health & ejection** — active health checks + passive failure tracking, eject, drain, reroute,
   re-admit on recovery.

---

## 5. The routing policies (this is the experiment)

All policies run against an **identical vLLM configuration**. Only the router varies. Hold
constant: prefix caching enabled, `--gpu-memory-utilization`, chunked prefill setting,
`--max-num-seqs`, CUDA graph settings, model, quantization, and the forced `awq:marlin` backend.

| # | Policy | Represents |
|---|---|---|
| 1 | **Round-robin** | the naive baseline |
| 2 | **Least-outstanding** (by inflight) | what nginx / a k8s Service actually gives you |
| 3 | **Consistent-hash session affinity** | **the baseline that matters** — sticky sessions, free in any LB |
| 4 | **Prefix-affinity + KV-pressure spill** | yours |
| 5 | **KV-event-driven exact residency** | *stretch* — what the approximation costs |

### Policy 3 is not optional
Sticky sessions capture most multi-turn cache locality with zero prefix tracking, and every load
balancer ships them. **If the trie only beats round-robin, you built a radix tree to beat a straw
man.** Beating policy 3 — or honestly reporting that you did not — is the actual result. Because
the session ID arrives as a header, policy 3 gets a perfect oracle, which makes it *stronger*
than it would be in production; beating it therefore counts for more.

Where policy 4 should beat policy 3, and what the workload must therefore contain:
- **shared system prompts** across otherwise-unrelated sessions
- **branched / regenerated** conversations sharing a common ancestor
- **RAG-style shared context blocks** reused across sessions
- **rebalancing after replica loss** — consistent hashing rehashes and wrecks its cache; a prefix
  index degrades gracefully (measured in §7)

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
head-to-head answers a question **none of the prior-art systems publish**: what does the
approximation actually cost? Build it only after policy 4 is measured; it must not delay the
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
aggregate KV. Against a measured ~688k-token fleet capacity that is roughly 86 / 344 / 1,030 /
2,750 sessions; rescale off the real `num_gpu_blocks`. WS creates **memory pressure**, which is
what drives eviction and fires the `KV_HIGH_WATER` branch of the spill rule.

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
- Prometheus on **:9091**; 9090 belongs to another user.
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
7. **Prior art and how this differs** (§1) — cite SGLang router, NVIDIA Dynamo, llm-d, AIBrix, **Mooncake**, and **OpenAI's `prompt_cache_key`** explicitly. The last is the single most important citation: it is session affinity productized by a frontier lab, which is precisely why policy 3 is the baseline that matters
8. Reproduce: `make up`, `make bench`
9. Methodology: what was held constant, **how the SLO threshold was derived**, closed-loop vs open-loop, contamination handling, replica symmetry check
10. Belief divergence: prefix match vs actual computed prefill tokens
11. Negative and null results, stated plainly — the PCIe/disagg arithmetic (§8), and "prefix
    affinity did not beat session affinity in regime X" if that is what happened. Under the §0
    framing these are findings in the same voice as the positive ones, not an appendix
