# Fleet bring-up, 2026-09-06

The first run of all six replicas together, on `nlp-gpu-01.be.ucsc.edu`. Kept because the README's
"First measurements" table cites it, and a figure whose rows have been deleted is an assertion
rather than a measurement.

**Not a benchmark.** Two 20-second cells at concurrency 1 and 8, round-robin only, on the fixed
workload — which shares no prefixes between requests and so cannot exercise cache locality at all.
It measures that the fleet and the harness work end to end, and gives the hardware floor a
reference point.

## Files

| File | What it is |
|---|---|
| `cells/*.jsonl` | per-request rows, one JSON object per line — **the system of record** |
| `cells/*.json` | the cell summary and its contamination evidence |
| `router.jsonl` | the router's own rows: accept-to-dispatch overhead, joins to the above on `request_id` |
| `*.parquet` | the same three, compacted; what to read for analysis |
| `results.md` | the generated results table |
| `engine-startup.txt` | each replica's own log lines for backend, model load and KV capacity |

## Conditions

vLLM 0.28.0, `Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, 6× RTX 3090, one replica per GPU, ports
8000–8005. `--quantization awq_marlin --linear-backend marlin`, `VLLM_USE_FLASHINFER_SAMPLER=0`,
`--kv-cache-metrics`, prefix caching and chunked prefill on, `--max-model-len 8192`,
`--gpu-memory-utilization 0.9` — all from `ops/versions.env`. Driver: closed-loop, 2048-byte
prompts, `max_tokens` 32, 4 s warm-up per cell, 2 warm-up requests per replica beforehand.
All six GPUs clean throughout (11 samples per cell, zero foreign processes).

## Headline figures

| Quantity | Value |
|---|---|
| Router overhead p50 / p99 / max | 135 µs / 318 µs / 1.34 ms, over 280 requests |
| TTFT p50 / p99 at concurrency 1 | 320 ms / 330 ms |
| TTFT p50 / p99 at concurrency 8 | 328 ms / 364 ms |
| Inter-token latency p50 | 7.6 ms |
| KV cache capacity | 125,952 tokens per replica, identical on all six; 755,712 fleet-wide |
| Throughput | 1.85 rps at c=1, 11.58 rps at c=8 |
| Outcomes | 219 measured requests, 219 successes, 0 dropped, 0 failed |

No SLO was applied, so goodput is not reported. Deriving it from the concurrency-1 floor above is
issue #10.

## The capacity discrepancy, and one unconfirmed hypothesis

ADR-0001 recorded **119,408 tokens** from a single replica at first contact. Every replica here
reports **125,952** — 5.5% higher, same engine settings.

`engine-startup.txt` contains a possible explanation, **which has not been tested**: engine init
took **13.16 s with 0.79 s of compilation** in this run, against ADR-0001's 37.5 s with 18.2 s of
compilation. vLLM sizes the KV cache from memory left free after its profiling run, so if
`torch.compile` was still working during that profile on the first bring-up, less memory would have
looked available and the cache would have been sized smaller. A warm compile cache would then
explain a larger KV cache on every subsequent start.

That is a hypothesis with a plausible mechanism, not a finding. Confirming or discarding it against
`num_gpu_blocks` is an acceptance criterion of #10. Do not scale working set ratios off either
number until it is settled.
