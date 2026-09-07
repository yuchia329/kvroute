# Prior art: KV-cache-aware routing — primary-source verification

**Checked:** 2026-09-06 · **Verifies:** `idea.md` §0 and §1 · **Method:** primary sources only —
official docs, arXiv/proceedings PDFs, first-party engineering blogs, and the projects' own
repository source read through the GitHub API.

> ⚠️ **This field moves monthly.** Three of the eleven claims below were already stale against a
> May 2026 recall after ~4 months. Re-run this check before any README publish or interview loop,
> and re-date the file. Where a quote came out of an automated fetch of a primary page rather than
> a hand-read PDF, the URL is given so it can be eyeballed in seconds.

---

## Summary — what changed versus `idea.md` §1

Corrections first, in order of how much they cost if repeated wrong.

1. **§0's central positioning claim is REFUTED.** `idea.md` §0 says: *"every vendor publishes wins
   against round-robin, nobody publishes where the sophisticated thing stops paying against the
   baseline a frontier lab actually shipped."* That is no longer true. At least three published
   sources now run **cache-aware routing against sticky / consistent-hash session affinity as an
   explicit baseline**, and two of them publish where affinity *loses*:
   - **Anyscale / Ray Serve LLM (2026-08-25)** benchmarks `ConsistentHashRouter` head-to-head
     against `PureKVCacheAffinityRouter` and `KVAwareRouter` on one harness, and reports the
     cache-aware router winning on p99 *despite a lower prefix cache hit rate*.
   - **CacheRoute (arXiv 2608.19677, 2026-08-20)** uses "sticky consistent hashing" and
     "consistent hashing with bounded loads" as two of five baselines, and §5.3 publishes an
     operating envelope in which *"affinity can reduce capacity when the recoverable prefix work
     is small"* — capacity falling to 0.50–0.67× on one workload.
   - **llm-d, "Sticky Until Saturated" (2026-08-17)** publishes an explicit numeric saturation
     threshold at which prefix stickiness is abandoned.

   The project is **not** dead, but the differentiation must be re-cut. See
   "Suggested rewrite of §1" for replacement framing.

2. **OpenAI `prompt_cache_key` is weaker than §1 states, and the surrounding facts changed.**
   The docs do say it influences routing — but explicitly: *"Keys influence routing; they do not
   pin requests to a machine or guarantee a cache read hit."* More importantly, **OpenAI's default
   routing is already a prefix hash**, and `prompt_cache_key` is a disambiguator layered on top of
   it, not the primary affinity mechanism. Discount is now stated as **up to 90%**; minimum
   cacheable prompt is **1,024 tokens (GPT-5.6+) / 2,048 (older)**; reuse window is **30 minutes**
   on GPT-5.6+ with a `prompt_cache_retention: "24h"` option; a single cache machine overflows
   above **~15 requests/minute**.

3. **SGLang's router has been rewritten and the documented policy in §1 is now the *legacy* one.**
   `--cache-threshold`, `--balance-abs-threshold`, `--balance-rel-threshold` and the
   `cache_aware_zmq` policy have all been **removed** from the new `experimental/sgl-router`. Also:
   even in the still-shipping gateway, the below-threshold fallback is **smallest tree size**, not
   shortest queue — shortest queue is the *imbalance override*, a different branch.

4. **The llm-d / Gateway API Inference Extension endpoint picker has moved repositories.**
   EPP is now `llm-d/llm-d-router`; `kubernetes-sigs/gateway-api-inference-extension` keeps only a
   lightweight EPP, the InferencePool API and conformance tests. Citing the old repo path in a
   README will read as stale.

5. **`vLLM production-stack` should not be cited as a prefix-aware router.** Its own README lists
   routing algorithms as round-robin, session-ID-based, and **"Prefix-aware routing (WIP)"**.

6. **MiniMax is no longer "not reliably known."** MiniMax-M2 (arXiv 2605.26494, §6.2.6) publishes
   a *"cost-aware request router [that] dynamically balances queuing delay against cache migration
   costs, maximizing cache locality without overloading individual instances"* over a DFS-backed
   global KV cache. (Caveat: described in their agent-RL rollout stack, not stated to be the
   production API serving path.)

7. **Dynamo's cost function is no longer the two-term formula in most write-ups.** It now carries
   `overlap_score_credit` with a decay factor, separate host/disk cache-hit weights,
   `prefill_load_scale`, and an optional `decode_active_request_weight`. Quote the current one or
   quote none.

Everything else in §1 survives. Mooncake in particular is confirmed in detail and is the best
citation in the table — and, usefully, **Mooncake's own baselines were random and load-balancing
only**, which is still a true and defensible thing to say about that paper.

---

## A. OpenAI `prompt_cache_key`

**Claim as written (§1 table):** *"Session affinity, productized. An API parameter whose documented
purpose is routing requests sharing a key to the same machine to raise cache hit rates. This is
policy 3, shipped by a frontier lab."*

**Verdict: PARTLY CONFIRMED — directionally right, materially overstated.**

The parameter exists and *is* documented as routing-influencing, so this is not a fabrication. Two
things need fixing:

1. It is a **hint, not a pin.** The docs state the limit in one sentence.
2. It is **not the primary affinity mechanism.** OpenAI already routes by prefix hash by default;
   `prompt_cache_key` is combined with that hash to disambiguate and to mitigate overflow. So the
   frontier-lab-shipped baseline is closer to **prefix-hash routing** than to *session*
   stickiness — which, read carefully, is *better* for this project's story, not worse: it means
   the shipped production baseline is nearer policy 4's family than policy 3's.

### Evidence — verbatim from the docs

From the **Prompt cache keys** section:

> "Set `prompt_cache_key` to help requests with the same prefix reach the same cache. Keys
> influence routing; they do not pin requests to a machine or guarantee a cache read hit."

From the **Cache location** section — the part that shows prefix-hash routing is the default:

> "Cached states live on individual machines, where traffic above 15 requests per minute can lead
> to overflow routing."

with routing described as depending on "Current machine load and available capacity" and "A hash of
the initial tokens after the hidden OpenAI content, including tool definitions when present". And
on overflow:

> "requests may overflow to another machine. If that machine does not have a matching cache entry,
> the initial overflow request incurs a cache miss."

**Discount:** "Pay the model's reduced cached-input rate for reused tokens, discounted up to 90%."

**Minimum prompt length:** "The minimum cacheable prompt length is 1,024 tokens for GPT-5.6 and
later and 2,048 tokens for models older than GPT-5.6."

**TTL:** "A cached prefix remains eligible for reuse for 30 minutes after its most recent write or
reuse, though OpenAI may retain it longer." Earlier models: entries typically stay active for
around 5–10 minutes of inactivity, up to one hour; `prompt_cache_retention` accepts `"in_memory"`
or `"24h"`.

**Was `user` the earlier guidance? — CONFIRMED, from the API reference itself:**

- `prompt_cache_key`: *"Used by OpenAI to cache responses for similar requests to optimize your
  cache hit rates. Replaces the `user` field."*
- `user` (deprecated): *"A stable identifier for your end-users. Used to boost cache hit rates by
  better bucketing similar requests and to help OpenAI detect and prevent abuse."*
- `safety_identifier`: *"A stable identifier used to help detect users of your application that may
  be violating OpenAI's usage policies…"*

So the deprecated `user` field genuinely did double duty as a cache-bucketing hint, and OpenAI
split it into `prompt_cache_key` (cache routing) and `safety_identifier` (abuse detection). The §1
claim about earlier `user`-field guidance is **CONFIRMED**.

**Sources**
- https://developers.openai.com/api/docs/guides/prompt-caching
- https://developers.openai.com/api/reference/python/resources/chat/subresources/completions/methods/create

---

## B. Mooncake (Moonshot AI / Kimi)

**Claim as written:** *"Published frontier-lab design. Global scheduler estimates prefix hit length
per instance, balances against instance load, and rejects early on predicted load — the spill rule
generalized into admission control. The closest published relative of this project."*

**Verdict: CONFIRMED**, in every part, with better-than-claimed detail.

**The paper.** *Mooncake: A KVCache-centric Disaggregated Architecture for LLM Serving*, Ruoyu Qin,
Zheming Li, Weiran He, Mingxing Zhang, Yongwei Wu, Weimin Zheng, Xinran Xu (Moonshot AI + Tsinghua).
arXiv:2407.00079, v4 dated 3 Sep 2025. A companion paper — *Mooncake: Trading More Storage for Less
Computation — A KVCache-centric Architecture for Serving LLM Chatbot* — appeared at **USENIX FAST
'25**. Cite the arXiv version for the scheduler detail below; that is what I read.

**Conductor exists and is the global scheduler.** §6:

> "In this section, we mainly discuss how Conductor schedules the requests and KVCache blocks under
> normal conditions, leaving the discussion on overload scenarios for the next section."

**Prefix-hit-length estimation balanced against load — §6.1, verbatim:**

> "In Mooncake, however, the selection of prefill instances considers additional factors—not just
> load but also the prefix cache hit length and the distribution of reusable KVCache blocks. While
> there is a preference to route requests to prefill instances with longer prefix cache lengths to
> reduce computation costs, it may be beneficial to schedule them to other nodes to ensure overall
> system balance and meet TTFT SLOs."

and the mechanism:

> "For every new request, its input tokens are divided into several blocks, and a hash key is
> computed for each block. This involves generating a hash key of tokens in a block concatenated
> with the hash key of the previous block (if available). The request's block keys are then compared
> one by one against each prefill instance's cache keys to identify the prefix match length
> (prefix_len). … Conductor estimates the corresponding execution time based on the request length
> and prefix_len (which varies by instance). It then adds the estimated waiting time for that
> request to get the TTFT on that instance. Finally, Conductor assigns the request to the instance
> with the shortest TTFT."

**Algorithm 1** (verbatim structure) is worth knowing cold, because it is a direct ancestor of
policy 4's spill rule:

```
best_prefix_len, best_matched_instance ← FindBestPrefixMatch(P, block_keys)
for instance ∈ P:
    prefix_len ← instance.prefix_len
    T_queue   ← EstimatePrefillQueueTime(instance)
    if best_prefix_len / prefix_len < kvcache_balancing_threshold:      # cache-aware
        T_prefill ← EstimatePrefillExecutionTime(len(prompt), prefix_len)
        TTFT ← min(TTFT, T_queue + T_prefill)
    else:                                                              # cache-aware AND -balancing
        transfer_len ← best_prefix_len − prefix_len
        T_transfer ← EstimateKVCacheTransferTime(instance, best_matched_instance, transfer_len)
        T_prefill  ← EstimatePrefillExecutionTime(len(prompt), best_prefix_len)
        TTFT ← min(TTFT, T_transfer + T_queue + T_prefill)
d, TBT ← SelectDecodingInstance(D)                                     # load-balancing decode
if TTFT > TTFT_SLO or TBT > TBT_SLO: reject R
```

Note the structural difference from policy 4: Mooncake's escape hatch is not "spill to
least-loaded" but "route elsewhere **and migrate the KV blocks there**", priced with an estimated
transfer time. That is a meaningfully different design and a good interview contrast — kvroute has
no KV transfer path, so its only escape hatch is to pay the prefill.

**Early rejection — CONFIRMED, §7.2, verbatim:**

> "Upon the arrival of a request, Conductor evaluates whether to accept the request based on the
> greater load between the prefill and decoding pools. Early Rejection significantly reduces
> ineffective computations from rejected requests and enhances load balancing."

and from §6.1: "If the SLO is not achievable, Conductor directly returns the HTTP 429 Too Many
Requests response status code to the upper layers." §7.3–7.4 then document that naive early
rejection *induces anti-phase load oscillation* between prefill and decode pools, fixed by
predicting the decoding load rather than reading it. That oscillation story is a strong thing to
know and is not in §1.

**Mooncake's own baselines — relevant to claim K.** §6.2:

> "we conducted a scheduling experiment that compares random scheduling and load-balancing
> scheduling with our strategy. … In random scheduling, a prefill instance is selected arbitrarily
> for each request. In load-balancing scheduling, the instance with the lightest load is chosen."

8 prefill + 8 decode instances, 23,000 replayed real requests. **No sticky/consistent-hash
baseline.** So the §1 sentence about Mooncake specifically is still fair.

**Sources**
- https://arxiv.org/abs/2407.00079 (v4, 2025-09-03) — full text read from the PDF
- https://www.usenix.org/system/files/fast25-qin.pdf (FAST '25 companion; 403s to automated fetch,
  reachable in a browser)

---

## C. NVIDIA Dynamo KV-aware router

**Claim as written:** *"KV-aware router; consumes KV cache events from workers to track block
residency."*

**Verdict: CONFIRMED** on the mechanism. The **cost function has changed** and is now considerably
richer than the two-term formula circulating in write-ups — do not quote the old one.

**KV events → global prefix tree.** From `docs/.../router/router-design.md`, verbatim:

> "**Cached Blocks**: Maintained globally by the KvIndexer using a prefix tree built from
> worker-reported KV events. This provides accurate overlap information for routing decisions."

> "In Dynamo, we introduce a KVPublisher which emits KV Cache events that occur at each worker and
> a KVIndexer which keeps track of these events globally."

The two event types are "KV stored event" and "KV removed event", emitted at block allocation and
at eviction respectively. Active decode blocks are, by contrast, **tracked locally by the router**
across the request lifecycle — the same split kvroute uses between scraped KV utilization and
locally-counted inflight.

**Cost function — current, verbatim:**

```text
effective_device_credit = overlap_score_credit * overlap_score_credit_decay_factor
adjusted_prefill_blocks = max(0, (
    prefill_blocks
    - effective_device_credit * device_overlap_blocks
    - host_cache_hit_weight * host_overlap_blocks
    - disk_cache_hit_weight * disk_overlap_blocks
    - shared_cache_multiplier * shared_beyond_blocks
))
active_request_blocks = decode_active_request_weight * active_requests
cost = (
    prefill_load_scale * adjusted_prefill_blocks
    + potential_decode_blocks
    + active_request_blocks
)
```

with, verbatim: *"Higher overlap credits favor cache reuse (improving TTFT), while lower credits
prioritize even load distribution (improving ITL)"* and *"The router selects the worker with the
lowest cost."* `router_temperature` optionally softmax-samples over normalized cost logits to
spread load.

**This is the single best citation for §1's "the two real axes of variation" paragraph** — the
overlap credit *is* kvroute's `LOAD_IMBALANCE_FACTOR` in continuous form, and the docs say so in
one sentence. Older docs describe it as
`logit = kv_overlap_score_weight × potential_prefill_blocks + potential_active_blocks`; that shape
still appears in v0.7/v1.0 doc snapshots but is superseded on `main`.

**Sources**
- https://github.com/ai-dynamo/dynamo/blob/main/docs/fern/pages/developer-guide/knowledge-base/modular-components/router/router-design.md
- https://docs.nvidia.com/dynamo/dev/components/router/routing-concepts (version-pinned URLs move;
  the repo path above is the stable one)

---

## D. SGLang router

**Claim as written:** *"Rust, cache-aware load balancing over an approximate radix tree per worker"*
and (from the task brief) *"threshold on prefix match with a shortest-queue fallback, plus a
load-rebalancing guard."*

**Verdict: PARTLY CONFIRMED — accurate for the router being deprecated, stale for the replacement,
and one detail of the fallback was wrong all along.**

There are now **two** routers in `sgl-project/sglang`:

### D1. `sgl-model-gateway` (SMG) — the one the docs describe, currently being deprecated

Rust: yes. Approximate radix tree per worker: yes. The file header of
`sgl-model-gateway/src/policies/cache_aware.rs` is the primary source and states the policy
verbatim:

> "This strategy maintains an approximate radix tree for each worker based on request history,
> eliminating the need for direct cache state queries. The tree stores raw text characters instead
> of token IDs to avoid tokenization overhead.
>
> Process:
> a. For each request, find the worker with the highest prefix match
> b. If match rate > cache_threshold: Route to the worker with highest match (likely has relevant
>    data cached)
> c. If match rate ≤ cache_threshold: **Route to the worker with smallest tree size (most available
>    cache capacity)**
> d. Background maintenance: Periodically evict least recently used leaf nodes to prevent memory
>    overflow"

and the imbalance guard, separately:

> "The router dynamically switches between these strategies based on load conditions:
> - Uses load balancing when the system is imbalanced
> - Uses cache-aware routing when the system is balanced
>
> A system is considered imbalanced if both conditions are met:
> 1. (max - min) > abs_threshold
> 2. max > rel_threshold * min"

**The correction:** the below-threshold fallback is **smallest tree size**, not shortest queue.
Shortest queue is what runs when the *imbalance* test fires — a different branch with a different
trigger. Saying "threshold with a shortest-queue fallback" collapses two mechanisms into one and an
SGLang contributor would catch it.

SMG also ships a `consistent_hashing.rs` policy with an `X-SMG-Routing-Key` header for session
affinity — i.e. SGLang has both of kvroute's policy 3 and policy 4 in the same binary. It does not
publish a comparison between them (see K).

### D2. `experimental/sgl-router` — the replacement, and it invalidates the tuning story

From its README, verbatim:

> "The `cache_aware_zmq` policy has been removed. Configurations using it should select
> `--policy cache_aware` and choose a native cache-prefix source: the Router-local radix tree (the
> default), or the external Indexer shown above.
>
> The legacy `--cache-threshold`, `--balance-abs-threshold`, and `--balance-rel-threshold` flags
> have also been removed. They do not have one-to-one replacements."

The new `CacheAwarePolicy` (`experimental/sgl-router/src/policies/cache_aware.rs`) instead:
- gates candidates on `cache_affinity_min_matched_tokens` and `cache_affinity_min_match_ratio`;
- ranks by `matched_prefix_tokens`, tie-broken by `compare_prefill_pressure`;
- truncates to a bounded candidate set (`cache_candidate_ratio` / min / max workers);
- carries a **pressure guard** (`pressure_abs_threshold_tokens`, `pressure_abs_threshold_ms`,
  `pressure_rel_threshold`) and a `cache_switch_margin_tokens`;
- **falls back to power-of-two-choices**, not shortest queue, when affinity lookup yields nothing.

It also ships `sticky.rs` (routing-key pin with idle eviction; the doc comment explicitly notes it
is *not* consistent hashing and is per-router-instance state) and `session_aware.rs`. Prefix signal
comes either from a router-local `HashTree` over token-block hashes (`prefix_provider.rs`) or from
an **external gRPC `sgl-kv-indexer`** — the same approximate-vs-exact axis kvroute's policy 5
targets.

**Sources**
- https://github.com/sgl-project/sglang/blob/main/sgl-model-gateway/src/policies/cache_aware.rs
- https://github.com/sgl-project/sglang/blob/main/experimental/sgl-router/README.md
- https://github.com/sgl-project/sglang/blob/main/experimental/sgl-router/src/policies/cache_aware.rs
- https://lmsys.org/blog/2024-12-04-sglang-v0-4/ (2024-12-04; "Built in pure Rust", approximate
  radix tree, and the original round-robin benchmark: 82,665 → 158,596 tok/s, 20% → 75% hit rate)
- https://docs.sglang.io/docs/advanced_features/sgl_model_gateway

---

## E. llm-d / Gateway API Inference Extension

**Claim as written:** *"Kubernetes endpoint-picker with prefix-cache-aware scoring."*

**Verdict: CONFIRMED**, but **the repository moved** and the plugin taxonomy is finer than "a
scorer".

**The move.** From the GIE README, verbatim:

> "The Endpoint Picker (EPP), InferenceObjective and InferenceModelRewrite APIs, and Body Based
> Router (BBR) packages have moved to new repositories:
> - EPP and associated APIs: llm-d/llm-d-router
> - BBR: llm-d/llm-d-inference-payload-processor
>
> No new code will be accepted to these packages in this repository, and they will be archived
> soon. … This repository will continue to host the lightweight EPP (LWEPP) and the InferencePool
> API."

**Pluggable scorers: CONFIRMED.** The pipeline splits into `DataProducer` plugins that compute
per-endpoint prefix-match attributes and a generic `prefix-cache-scorer` (plus a
`prefix-cache-affinity-filter`) that consume them. Three producers exist:
`approx-prefix-cache-producer`, `precise-prefix-cache-producer`, and `burstprefix`.

**Approximate is the default: CONFIRMED.** From the precise producer's README:

> "Without the `prefixMatchInfoProducerName` field, the scorer falls back to the auto-spawned approx
> producer."

The approximate producer, verbatim: *"hashes the token IDs into fixed-size blocks, and looks up
which endpoints have recently served requests with a matching prefix … then records the selected
endpoint(s) in the index after scheduling completes"* — i.e. routing-history-derived belief, exactly
kvroute's prefix index. Defaults: `blockSizeTokens` 16 (clamped up to 64 at request time),
`maxPrefixTokensToMatch` 131072, `lruCapacityPerServer` 31250, `autoTune` true.

**Precise KV-event mode is optional: CONFIRMED.** The precise producer owns "the precise KV-block
index", consumes per-pod **ZMQ** KV events (`kvEventsConfig.engineType` is `vllm` by default,
`sglang` supported), does its own block hashing from `TokenizedPrompt`, and offers optional
`speculativeIndexing` with a 2 s TTL. Its README also documents a real cross-engine limitation
worth knowing: SGLang does not emit `extra_keys`, so `cache_salt`-ed requests are precise-cache
misses there.

**Published approximate-vs-precise numbers exist** (llm-d blog, 2025-09-24): precise scheduling
P90 TTFT 0.542 s vs approximate 31.083 s (~57×), throughput 8,730 tok/s (+25% over approximate,
~2× over cache-blind). Baselines in that post: random, load-only, approximate, precise. **This
directly overlaps kvroute's policy-5 stretch goal** — the "what does the approximation cost"
question is already answered in one regime by llm-d. Policy 5 is still worth doing on this
hardware class, but it must be framed as a *replication at a different scale*, not as a new
question.

**Sources**
- https://github.com/kubernetes-sigs/gateway-api-inference-extension (README, repo-move notice)
- https://github.com/llm-d/llm-d-router/blob/main/pkg/epp/framework/plugins/requestcontrol/dataproducer/approximateprefix/README.md
- https://github.com/llm-d/llm-d-router/blob/main/pkg/epp/framework/plugins/requestcontrol/dataproducer/preciseprefixcache/README.md
- https://llm-d.ai/blog/kvcache-wins-you-can-see (2025-09-24)

---

## F. AIBrix and vLLM production-stack

### F1. AIBrix — **CONFIRMED**

*AIBrix: Towards Scalable, Cost-Effective Large Language Model Inference Infrastructure*,
arXiv:2504.03648 (submitted 2025-02-22), the AIBrix Team. The abstract names *"prefix-aware,
load-aware routing"* and *"a distributed KV cache, boosting token reuse across nodes, leading to a
50% increase in throughput."*

The design doc is more useful than the paper for §1. `docs/source/designs/aibrix-router.rst`,
verbatim:

> "`prefix-cache`: routes to a pod that already has a KV cache matching the request's prompt
> prefix; the best prefix-matched pod within a stddev load threshold is selected. Purely about
> prefix caching — it does not itself gate on cluster-wide load imbalance (the gateway applies that
> gate centrally ahead of it…). Supports standard mode (local hash table) and KV sync mode
> (real-time distributed index, enabled via `AIBRIX_PREFIX_CACHE_KV_EVENT_SYNC_ENABLED=true`)."

So AIBrix has the **same approximate-vs-exact axis** as llm-d and kvroute's policy 4/5 split, and
the load-imbalance gate is applied *centrally by the gateway ahead of whichever strategy routes* —
architecturally cleaner than folding it into the policy, and a fair thing to note as a design
alternative to kvroute's inline spill rule. It also ships `prefix_cache_preble.go`, a Preble-style
variant.

One-line characterization for §1: *"Envoy-gateway ext-proc router with a `prefix-cache` strategy
(local hash table or KV-event-synced distributed index), selecting the best prefix match within a
standard-deviation load threshold, behind a central load-imbalance gate."*

### F2. vLLM production-stack — **PARTLY CONFIRMED / must be re-worded**

Its README lists, verbatim:

> "Multiple routing algorithms:
> - Round-robin routing
> - Session-ID based routing
> - **Prefix-aware routing (WIP)**"

and lists "KV-cache-aware routing algorithm" under *future* "Router improvements". The component
description is *"Directs requests to appropriate backends based on routing keys or session IDs to
maximize KV cache reuse."*

So production-stack is a **reference router with session-ID stickiness plus observability** — a
citation for *policy 3 being the shipped default in the vLLM ecosystem*, which is a better use of it
anyway. Do not cite it as prefix-aware.

**Sources**
- https://arxiv.org/abs/2504.03648
- https://github.com/vllm-project/aibrix/blob/main/docs/source/designs/aibrix-router.rst
- https://github.com/vllm-project/production-stack (README)

---

## G. Anthropic `cache_control` and Google Gemini context caching

**Claim as written:** *"about what to cache (explicit breakpoints / cache handles), a different axis
from where to route."*

**Verdict: CONFIRMED** for the framing. Neither provider's caching documentation mentions routing at
all. Numbers below.

### Anthropic — verified

- **Mechanism:** `cache_control: {"type": "ephemeral"}` placed on individual content blocks, or a
  single top-level `cache_control` for automatic caching. **Max 4 explicit cache breakpoints per
  request.** Prefix match: any byte change in the prefix invalidates everything after it; render
  order is `tools` → `system` → `messages`.
- **TTL:** default **5 minutes**; **1 hour** option via `{"type": "ephemeral", "ttl": "1h"}`.
- **Pricing multipliers:** 5-minute cache write **1.25×** base input; 1-hour cache write **2×**;
  **cache read 0.1×** (0.025× on Claude Fable 5.1 / Mythos 5.1).
- **Minimum cacheable prompt:** model-dependent, 512 → 4,096 tokens (512 for Fable 5.1 / Mythos 5.1 /
  Opus 5 / Fable 5 / Mythos 5; 1,024 for Opus 4.8 / Sonnet 5 / Sonnet 4.6 / Sonnet 4.5; 2,048 for
  Opus 4.7; 4,096 for Haiku 4.5).
- **Routing:** the prompt-caching documentation contains **no** statement about routing requests to
  particular machines. The framing in §1 holds.

### Google Gemini — mostly verified, one number UNVERIFIED

- **Implicit caching:** *"Implicit caching is enabled by default for all Gemini 2.5 and newer
  models."* Minimum thresholds 2,048 tokens (Gemini 2.5 Flash/Pro) to 4,096 (3.x models).
- **Explicit caching:** a `CachedContent` handle created with `client.caches.create()`. TTL:
  *"If not set, the TTL defaults to 1 hour."* Minimum input tokens for explicit caching match the
  implicit thresholds (2,048 / 4,096 by model). Billing is *"Cache token count … billed at a reduced
  rate when included in subsequent prompts"* plus *"Storage duration … billed based on the TTL
  duration of cached token count."*
- **⚠️ UNVERIFIED:** the exact explicit-cache discount (a commonly cited **90% on Gemini 2.5+, 75%
  on Gemini 2.0**) and the per-hour storage rate. The caching guide defers to the pricing page,
  which I did not fetch, and the figures above came out of search-result text rather than a fetched
  primary page. **Do not put a Gemini discount number in the README without opening
  https://ai.google.dev/pricing first.**
- **Routing:** no mention. Framing holds.

Also worth noting for the framing itself: the *"what to cache"* vs *"where to route"* split is real,
but Gemini's implicit caching and Anthropic's automatic top-level `cache_control` both blur it a
little — they cache without the caller declaring anything, which makes the "explicit breakpoints"
half of the §1 sentence less true than it was. Phrase it as *"cache lifetime and scope"* rather than
*"explicit breakpoints"* and it survives cleanly.

**Sources**
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://ai.google.dev/gemini-api/docs/caching
- https://ai.google.dev/gemini-api/docs/generate-content/caching

---

## H. DeepSeek

**Claims:** disk-backed context caching with cache-hit pricing; prefill/decode disaggregation;
anything published on their *request-routing* layer.

**Verdict: CONFIRMED on caching and disaggregation. Nothing published on prefix-aware request
routing — the §1 position holds.**

**Context Caching on Disk** (announced 2024-08-02, DeepSeek API docs "news" post). Verbatim
elements: caches reusable content *"on a distributed disk array"*; *"64 tokens as a storage unit;
content less than 64 tokens will not be cached"*; *"only requests with identical prefixes (starting
from the 0th token) will be considered duplicates. Partial matches in the middle of the input will
not trigger a cache hit"*; *"storage usage for the cache is free"*; unused entries *"automatically
cleared after a period"*, typically hours to days. Cache-hit input priced at a small fraction of
cache-miss input (announced at $0.014/M tokens; **current per-model rates move — check
https://api-docs.deepseek.com/quick_start/pricing before quoting a number**).

**Prefill/decode disaggregation — CONFIRMED**, from the DeepSeek-V3/R1 Inference System Overview
(open-infra-index, Feb 2025). Prefill: EP32, deployment unit spanning 4 nodes; decode: EP144,
spanning 18 nodes. Three load balancers, and this is the part that matters for §1 — verbatim
objectives:

- **Prefill Load Balancer:** *"Balance core-attention computation across GPUs"*; *"Equalize input
  token counts per GPU (dispatch send load balancing)"*
- **Decode Load Balancer:** *"Balance KVCache usage across GPUs"*; *"Equalize request counts per
  GPU"*
- **Expert-Parallel Load Balancer:** *"Balance expert computation on each GPU"*

**None of these is prefix-locality-aware.** They balance token counts, request counts, aggregate
KVCache *usage*, and expert dispatch. The same post reports 342B tokens (56.3%) hitting the on-disk
KV cache, but publishes no mechanism for how a request reaches the node holding its prefix. So:
**DeepSeek publishes the cache and the disaggregation, not the router.** That is a correct and
citable statement, and it is the sharper version of what §1 implies.

**Sources**
- https://api-docs.deepseek.com/news/news0802/
- https://github.com/deepseek-ai/open-infra-index/blob/main/202502OpenSourceWeek/day_6_one_more_thing_deepseekV3R1_inference_system_overview.md

---

## I. MiniMax

**Claim as written (§1 / brief):** *"not reliably known."*

**Verdict: REFUTED — correct this.**

*The MiniMax-M2 Series: Mini Activations Unleashing Max Real-World Intelligence*, MiniMax,
arXiv:2605.26494, submitted 2026-05-26. §6.2.6 "Inference Acceleration", under the heading
**"Global L3 KV Cache Pool"**, verbatim:

> "A distributed, DFS-backed global KV cache maximizes prefix cache hit rates through group-level
> rollout scheduling. A cost-aware request router dynamically balances queuing delay against cache
> migration costs, maximizing cache locality without overloading individual instances and avoiding
> redundant prefilling across the multi-turn interactions characteristic of agent RL."

That is, in one sentence, the same tradeoff kvroute is built to measure: cache locality against
queuing delay, with a cost term for moving the cache rather than recomputing it (like Mooncake, and
unlike kvroute).

**Two caveats, both of which should survive into any citation:**
1. The passage sits in the **agent-RL rollout infrastructure** discussion. It is not stated to be
   the production serving path for the public API. Say "their RL rollout serving stack" unless you
   find a second source.
2. It is one paragraph. There is no algorithm, no threshold, no ablation. It is a *disclosure*, not
   a *design*.

Also worth knowing: `ai-dynamo/dynamo` carries a `components/src/dynamo/thunderagent_router/` with a
`run_minimax_8xh100.sh` — i.e. NVIDIA ships a MiniMax-targeted router variant. Not evidence about
MiniMax's own serving layer, but it will come up if an interviewer greps.

**Sources**
- https://arxiv.org/abs/2605.26494 · §6.2.6 at https://arxiv.org/html/2605.26494v1

---

## J. vLLM KV cache events (0.28.0)

**Claim as written (§5, policy 5):** *"vLLM 0.28.0 ships `vllm/distributed/kv_events.py` with
`BlockStored`, `BlockRemoved`, `AllBlocksCleared` and `EventPublisher`."*

**Verdict: CONFIRMED**, read directly from the `v0.28.0` tag. 560 lines.

Present at that tag:

| Symbol | Kind |
|---|---|
| `EventBatch`, `KVEventBatch` | batch envelopes |
| `KVCacheEvent` | base event |
| `BlockStored`, `BlockRemoved`, `AllBlocksCleared` | the three event types |
| `EventPublisher` (ABC), `NullEventPublisher`, `ZmqEventPublisher` | publishers |
| `KVEventAggregator`, `KVConnectorKVEvents` (ABC) | multi-worker aggregation (newer than most write-ups) |

**How a router subscribes.** `ZmqEventPublisher` is documented in its own docstring as a
*"Reliable PUB/ROUTER publisher with an in-memory replay buffer"*:

- `endpoint` — PUB address, default `tcp://*:5557` to bind or `tcp://host:5557` to connect. The
  port is **offset per data-parallel rank** (`offset_endpoint_port(endpoint, dp_rank)`), so a
  multi-DP replica publishes on several ports.
- `replay_endpoint` — optional ROUTER address; subscribers that detect a sequence gap request
  replay from it.
- `buffer_steps` — default **10,000** past batches retained for replay.
- `topic` — ZMQ SUB topic prefix, default `""`.

So the router side is: ZMQ SUB on the PUB endpoint filtered by `topic`, msgpack-decode
`KVEventBatch`, track the monotonic sequence number, and on a gap send the missing start sequence to
the ROUTER socket, which streams back from the buffer and terminates with a `seq=-1` sentinel.

**Design gap worth knowing (it is the honest weakness of policy 5).** Dynamo's own comparison doc
states it plainly, verbatim about vLLM: *"No built-in initial state sync — a consumer that connects
after events have already been published starts with an empty view"* and *"If the gap is older than
the buffer window, the consumer must rebuild state through other means (e.g., restart and
re-discover)."* Dynamo added a RadixTree snapshot/`TreeDump` fallback for exactly this. A policy-5
router built on raw vLLM events therefore starts cold on every router restart and cannot fully
recover from a long gap — that is a real, citable limitation and a better answer than "events give
you exact residency."

**Sources**
- https://github.com/vllm-project/vllm/blob/v0.28.0/vllm/distributed/kv_events.py
- https://github.com/ai-dynamo/dynamo/blob/main/docs/fern/pages/developer-guide/knowledge-base/modular-components/router/kv-event-replay-comparison.md

---

## K. Has anyone published cache-aware routing vs session-sticky / consistent-hash routing?

**Claim as written (§0):** *"every vendor publishes wins against round-robin, nobody publishes where
the sophisticated thing stops paying against the baseline a frontier lab actually shipped"* — and
(§1) *"What is unpublished is the ablation."*

**Verdict: REFUTED.** This is the critical finding. Three independent sources published between
February and August 2026 do run cache-aware routing against a consistent-hash / sticky baseline, and
two of them publish where affinity loses. The §1 and §0 framing must change before the README ships.

Sources are ordered by how directly they collide with the project's claim.

### K1. Anyscale / Ray Serve LLM — the closest published comparison (2026-08-25)

*"Optimizing LLM Serving Efficiency: Moving Beyond KV Cache Reuse to Token-Load Awareness with Ray
Serve LLM"*. Compares **three routers on one harness**:

| Router | What it is |
|---|---|
| `ConsistentHashRouter` | session affinity via consistent hashing — **kvroute's policy 3** |
| `PureKVCacheAffinityRouter` | *"modifies `KVAwareRouter` to optimize solely for KV cache overlap"* |
| `KVAwareRouter` | *"considers both KV cache overlap and token load"* — **kvroute's policy 4** |

Workloads: multi-turn RL rollouts (8 rollouts × 10 turns, 2K-token seed, 1K/turn, two 8K
stragglers) at concurrency 16, and a filtered subset of the Weka Claude Code trace corpus on
gpt-oss-120b. Headline: *"`KVAwareRouter` has the best p99 rollout end-to-end latency **despite
lower prefix cache hit rate**"*, and on the Claude Code trace it achieves *"better TTFT, TPOT, and
throughput than session affinity with consistent hashing"* by trading hit rate for load balance.

The post also names where consistent hashing remains preferable — expensive cache misses, limited
cross-session reuse, and similar-sized sessions where *"consistent hashing already provides
reasonable load balance"*.

**That is the ablation §1 claims is unpublished, run against exactly the baseline §5 nominates, with
the same headline mechanism (hit rate traded for load balance).** The corresponding Ray docs page
documents `PrefixCacheAffinityRouter`'s policy: an `imbalanced_threshold` on queue-length spread
gates prefix routing at all; above a `match_rate_threshold` (default 0.1) route to best match; below
it route to lowest prefix-cache utilization; otherwise power-of-two-choices — i.e. an independently
arrived-at policy 4.

### K2. CacheRoute (arXiv 2608.19677, 2026-08-20) — publishes where affinity *loses*

*CacheRoute: Planned Prefix-Affinity Routing for Large-Scale LLM Serving*. **Five baselines**, per
§4:

1. `Flat-LB` — power-of-two-choices
2. **`Sticky` — sticky consistent hashing** (Karger et al. 1997)
3. `CHWBL` — consistent hashing with bounded loads, ε = 0.25
4. `DualMap` — two-candidate cache/load policy
5. `Preble` — prefix-history + live-load

Primary result (70B fp8, 60 H100s, 3.5 s SLO): CacheRoute 176 ± 11 QPS at 93.2% KV hit; Preble
(strongest baseline) 76 ± 11 QPS at 72.0%; **Sticky 30 QPS at 87.3% KV hit** — the highest hit rate
among the baselines and the *lowest* capacity, which is precisely the "hit rate is not the outcome"
result. The paper's own line: *"locality alone does not help once a queue dominates the tail."*

§5.3 is the operating-envelope section and is the direct competitor to kvroute's pressure map.
Verbatim: *"For 32B aggregate workload A, affinity moves KV hit only from 1.1% to 11.8%. That
improvement does not outweigh the remaining skew, and capacity falls to 0.50–0.67×"*, summarized as
*"affinity can reduce capacity when the recoverable prefix work is small."* They also report an
eviction sweep and recommend gating deployment with a shadow replay rather than enabling affinity
from workload statistics alone.

**This is a published map of where cache-aware routing stops paying.** It is not the *same* map —
theirs is parameterized by recoverable prefix work and key skew at 60-GPU scale, not by working-set
ratio over aggregate KV on a 6-GPU single host — but the claim "nobody publishes this" is dead.

### K3. DualMap (arXiv 2602.06502, 2026-02-06) — cache affinity as an explicit baseline

*DualMap: Enabling Both Cache Affinity and Load Balancing for Distributed LLM Serving* (Ying Yuan,
Pengfei Zuo, Bo Wang, Zhangyu Chen, Zhipeng Tan, Zhou Yu). Maps each request to **two candidate
instances via two independent hash functions** and picks by system state; adds SLO-aware routing,
hotspot rebalancing and dual-hash-ring scaling. Baselines: **Cache Affinity**, Least Loaded, Min
TTFT, Preble. Up to **2.25×** effective request capacity under the same TTFT SLO; P50 TTFT down
55.4–97.4%; goodput +16.7–48% (Tool&Agent) and +14.3–40% (Conversation).

DualMap is a *hash-ring* system, so the whole paper is an argument about the consistent-hashing
family versus cache-aware routing.

### K4. llm-d, "Sticky Until Saturated: Token-Aware Routing in llm-d" (2026-08-17)

Publishes a **numeric saturation threshold at which prefix stickiness is abandoned**: τ = 286,720
tokens = "35 × B", which *"fires when keeping a sticky request would cause new arrivals to queue
~14 seconds of prefill work."* Five scheduler configurations on 10× Qwen3-32B (TP=2, 2× H100 each)
across three workloads; headline *"2–3× the throughput of Kubernetes Service round-robin on
prefill-bound workloads"* (46k vs 16k in_t/s at concurrency 60).

Its baseline is k8s round-robin, not consistent hashing — so it does not refute §0's *comparison*
claim. But it does refute the *"nobody publishes where stickiness stops paying"* half, in the most
literal possible way: the post's title is that finding, and the threshold is a published number.

### K5. Adjacent, weaker

- **AGENTSERVESIM** (arXiv 2606.09613, simulator, commodity CPUs) implements a Session-Aware Router
  and compares against round-robin and least-loaded. Simulation, not hardware; treat as supporting.
- **"Accuracy Is Speed"** (arXiv 2604.15732) uses *load-aware* and *session-affinity* routing as its
  two baselines, on the llm-d implementation — but its objective is model-accuracy routing, not
  cache locality.
- **brixbench** (`vllm-project/aibrix/brixbench/`) runs a cross-vendor comparison — AIBrix
  `ROUTING_ALGORITHM: pd` vs Dynamo **round-robin** vs llm-d, on a `prefix_repetition` workload
  (prefix 6000 / suffix 2000 / output 1024 / concurrency 32). Real, reproducible, cross-system —
  and its baseline is still round-robin.
- **vLLM blog, "Session-Aware Agentic Routing"** (2026-06-02) compares single-turn, sticky-session
  and SAAR variants — but that is *model selection* routing, not replica routing. Not a
  counterexample; do not cite it as one.
- **Preble** (arXiv 2407.00023) is the standard prefix-aware scheduling baseline everyone else
  compares to; it compares itself to SGLang and vLLM with naive load balancing, not to sticky.

### What survives

Nothing here runs the specific experiment kvroute proposes. **Every one of K1–K4 is at
multi-node, ≥8×H100/H200 scale with 32B–70B models.** None is on a single host with six
consumer-class 24 GB cards and a 4-bit 8B model; none parameterizes by **working-set ratio over
measured aggregate fleet KV**; none separates **memory pressure (WS) from load imbalance (Zipf
skew) as independent axes**; none publishes **belief divergence** between a router's model of
residency and the engine's actual computed prefill tokens. CacheRoute comes closest and
parameterizes by recoverable prefix work and key skew instead.

But the honest sentence is no longer *"nobody publishes this."* It is *"this has been published at
datacentre scale; nobody has published it at single-host scale, and nobody publishes the
belief-divergence measurement at all."* That is a narrower claim and it needs the narrower framing
to be defensible.

**Sources**
- https://www.anyscale.com/blog/llm-kv-token-aware-routing (2026-08-25)
- https://docs.ray.io/en/latest/serve/llm/user-guides/prefix-aware-routing.html
- https://arxiv.org/abs/2608.19677 · https://arxiv.org/html/2608.19677 (2026-08-20)
- https://arxiv.org/abs/2602.06502 (2026-02-06)
- https://llm-d.ai/blog/sticky-until-saturated-token-aware-routing (2026-08-17)
- https://arxiv.org/abs/2606.09613 · https://arxiv.org/abs/2604.15732 · https://arxiv.org/abs/2407.00023
- https://github.com/vllm-project/aibrix/tree/main/brixbench

---

## Suggested rewrite of §1

Replacement prose for the prior-art table and the paragraphs around it. Accurate as of 2026-09-06.

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

### One extra sentence for §0

The kill/validity condition in §0 should absorb the same correction. Replace *"every vendor
publishes wins against round-robin, nobody publishes where the sophisticated thing stops paying
against the baseline a frontier lab actually shipped"* with:

> Published comparisons against sticky/consistent-hash baselines now exist (Anyscale, CacheRoute,
> llm-d), all at datacentre scale. A null result here is still publishable — but as *"the crossover
> sits at WS X on a 6×3090 host, and here is the belief divergence that explains it,"* not as
> *"nobody has measured this."*

---

## Recency notes — what moved, and how fast

Everything in this list post-dates a May 2026 knowledge cutoff or moved within the last four months.
It is the reason §1 carries a re-verify warning, and the reason it should carry one permanently.

| What changed | When | Impact |
|---|---|---|
| Anyscale publishes `ConsistentHashRouter` vs `KVAwareRouter` head-to-head | 2026-08-25 | **Breaks §0's central claim** |
| CacheRoute publishes sticky-consistent-hashing baselines + a negative-result envelope | 2026-08-20 | **Breaks §0's central claim** |
| llm-d publishes a numeric stickiness saturation threshold | 2026-08-17 | Breaks the "where it stops paying" half |
| EPP moves out of `gateway-api-inference-extension` → `llm-d/llm-d-router` | recent; GIE README says the old packages "will be archived soon" | §1 citation path is stale |
| SGLang router rewritten as `experimental/sgl-router`; `cache_aware_zmq` and the three threshold flags removed; `sgl-model-gateway` being deprecated | source files carry 2026 copyright | §1's SGLang row describes a deprecating router |
| Dynamo cost function gains overlap-credit decay, host/disk cache weights, `prefill_load_scale`, `decode_active_request_weight` | ongoing on `main` | The widely-quoted two-term formula is superseded |
| MiniMax-M2 paper discloses a cost-aware cache-locality router | 2026-05-26 | "Not reliably known" is now wrong |
| OpenAI prompt-caching docs move to `developers.openai.com`; min length now model-tiered (1,024 for GPT-5.6+), discount stated as up to 90%, 30-minute reuse window, `prompt_cache_retention: "24h"` | current page | Every number in a `prompt_cache_key` claim needs re-checking per model generation |
| DualMap (hash-ring cache-affinity vs load-balancing) | 2026-02-06 | Another published sticky-family comparison |

**Re-check before publishing:** the three arXiv preprints (2608.19677, 2602.06502, 2606.09613) may
have been revised or accepted somewhere by the time this is read — cite the version you actually
read. The OpenAI numbers are the most volatile item on the list; the Gemini explicit-caching
discount is the one item still marked UNVERIFIED above.
