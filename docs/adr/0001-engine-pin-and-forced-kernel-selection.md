# ADR-0001: Pin the engine and force every kernel selection

**Status:** Accepted · **Date:** 2026-09-06 · **Verified on:** `nlp-gpu-01.be.ucsc.edu`

## Context

The experiment varies one thing — the router's policy — and holds the fleet identical. Anything
the engine chooses for itself can differ between cells, and a kernel that flips halfway through a
sweep would change what the comparison measures without changing anything visible in the results.
Completed cells would be silently invalidated.

`idea.md` §2 records the intent as "force `backend='awq:marlin'`". Bringing up a replica showed
that vLLM 0.28.0 does not have one such switch. It has three, in different places.

## Decision

Pin the engine in a project-dedicated venv and close **every** automatic selection, in
`ops/versions.env` as the single source of truth.

1. **`vllm==0.28.0`** in `~/.venvs/kvroute`, never shared with other work. `ops/replica.sh`
   refuses to start if the venv holds a different version, because a dependency bump made
   elsewhere on a shared box would otherwise invalidate finished cells quietly.

2. **`--quantization awq_marlin`** pins the quantization config class. Left unset, the model's own
   `quantization_config` is read and the `auto_awq` path decides whether to upgrade AWQ to Marlin.
   Confirmed on the box that `awq_marlin` is in `QUANTIZATION_METHODS` and is *not* in
   `DEPRECATED_QUANTIZATION_METHODS` (which holds only `fbgemm_fp8` and `fp_quant`).

3. **`--linear-backend marlin`** pins the GEMM kernel for quantized linear layers. This is a
   second, independent selection that 0.28.0 added, and it defaults to `auto` — described in the
   engine's own help as "automatically select the best backend based on model and hardware".
   Pinning the quantization method alone would have left it free to choose, which is exactly the
   failure this ADR exists to prevent.

4. **`VLLM_USE_FLASHINFER_SAMPLER=0`.** The FlashInfer sampler JIT-compiles a CUDA kernel on first
   use. On this box that fails outright — `nvcc` is not on `PATH` — but the deeper objection is
   that it puts a compile step, and a JIT cache that can differ between runs, inside every replica
   launch. Six staggered replicas would pay it six times, and a cell whose engine compiled its own
   sampler is not obviously comparable to one that reused a cache. The PyTorch sampler is used
   instead and held constant like every other engine setting.

5. **`--kv-cache-metrics`.** Off by default, and the only source of the KV residency histograms
   (`kv_block_lifetime_seconds`, `kv_block_idle_before_evict_seconds`, `kv_block_reuse_gap_seconds`)
   that `idea.md` §4.3 relies on to calibrate the prefix index's TTL against how fast replicas
   actually evict, rather than guessing it. Sampled at 1%, so the overhead is small. Turned on now
   even though nothing reads it yet, because turning it on later would change the engine
   configuration that every cell is supposed to share — the exact failure this ADR prevents.

Startup asserts (2) and (3) took effect by requiring the engine log to match
`QUANTIZATION_LOG_PATTERN`. A version that ignores or renames a flag fails at bring-up rather than
surfacing later as an unexplained shift in a benchmark cell.

## Evidence

From `run/replica-0.log` on first successful bring-up:

```
[auto_awq.py:448] Using MarlinLinearKernel for AutoAWQMarlinLinearMethod
[topk_topp_sampler.py:46] FlashInfer top-p/top-k sampling disabled via VLLM_USE_FLASHINFER_SAMPLER=0.
[model_runner.py:380] Model loading took 5.39 GiB memory and 5.049777 seconds
```

The venv resolved to `vllm 0.28.0` on `torch 2.13.0+cu130`, matching `idea.md` §2. Measured KV
capacity on one replica was **119,408 tokens**, against `idea.md` §2's hand estimate of ~114,700 —
within 4%.

> **Superseded, 2026-09-06, and explained, 2026-09-07.** A six-replica bring-up under
> `ops/fleet.sh` reported **125,952 tokens on every replica** with these same settings — 5.5% above
> the figure recorded here. The gap is the `torch.compile` cache, and it reproduces exactly:
>
> | | KV cache | engine init | compilation |
> |---|---:|---:|---:|
> | Warm compile cache | 125,952 tokens | 12.76 s | 0.26 s |
> | Cold compile cache (`VLLM_CACHE_ROOT` moved aside) | **119,408 tokens** | 38.55 s | **19.24 s** |
> | This ADR's first contact | **119,408 tokens** | 37.5 s | **18.2 s** |
>
> vLLM sizes the KV cache from the memory left free after its profiling pass, and on a cold cache
> `torch.compile` is still holding about 0.8 GiB when that pass runs. The figure recorded here was
> not wrong; it was measured on a fleet that had never compiled this model before.
>
> **The consequence is that KV capacity is not a property of the engine settings alone.** It is why
> `cmd/characterize` reads `num_gpu_blocks` off every replica at runtime, per run, rather than
> trusting a constant or a startup log from some earlier bring-up — see
> [the characterization](../measurements/2026-09-07-characterization/) and §2's capacity math.

Two corrections the live replica forced on `idea.md` §4.6, both caught by the contract test rather
than in a later sweep:

- **Every counter carries a `_total` suffix.** The spec recorded `vllm:prompt_tokens`; the engine
  serves `vllm:prompt_tokens_total`. Gauges and histograms are named as the spec had them.
- **The residency histograms are opt-in**, per (5) above. Without the flag they are absent
  entirely rather than present and zero, so a scrape would have silently found nothing.

## Consequences

- The replica cannot start on a drifted engine, and cannot start having quietly chosen a different
  kernel. Both are loud failures at bring-up.
- The sampler is not the engine's default. This is a deviation worth stating in the README's
  methodology section alongside the chunked-prefill setting, since it is held constant across
  every policy and therefore cannot favour one.
- Five settings now have to be reproduced by anyone rerunning this. They live in one file.
- The residency metrics carry a 1% sampling cost from the first cell onward, paid on every policy
  equally, in exchange for not having to re-run the fleet when TTL calibration starts.

## Revisit if

- A later vLLM adds a fourth automatic selection on the quantized path.
- The FlashInfer sampler is wanted for its own sake, at which point `nvcc` must be on `PATH` at
  launch and the JIT cache must be warmed identically before any cell runs.
