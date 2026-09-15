# Characterization — 2026-09-07

Model `hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, workload `fixed(prompt=2048B,output=64t)`, 6 replicas driven one at a time, every other replica idle.

> ⚠️ **This characterization is flagged.** Every downstream figure scales off these
> numbers, so read the reasons before using any of them.

> - replica symmetry is unresolved at one or more levels: at concurrency 32, TTFT p50 spread 22.1% over the 8.0% tolerance: replica-5 at 1.292s against replica-4 at 1.058s; at concurrency 32 that spread is not resolvable: one replica varies 36.3% against itself between repetitions, wider than the 8.0% tolerance, so the difference between replicas is inside the measurement's own noise. Lengthen the probe or add repetitions rather than acting on it
> - 4 of 36 probes flagged: still warming up: the first half of the measured window was 26% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run (replica-1-c32-r1, replica-3-c32-r1, replica-5-c32-r1 and 1 more)

## Aggregate fleet KV capacity

Read off every replica's own `vllm:cache_config_info`, not extrapolated from one.

| replica | GPU | num_gpu_blocks | block size | tokens | KV bytes |
|---|---:|---:|---:|---:|---:|
| `replica-0` | 0 | 7,872 | 16 | 125,952 | 15.38 GiB |
| `replica-1` | 1 | 7,872 | 16 | 125,952 | 15.38 GiB |
| `replica-2` | 2 | 7,872 | 16 | 125,952 | 15.38 GiB |
| `replica-3` | 3 | 7,872 | 16 | 125,952 | 15.38 GiB |
| `replica-4` | 4 | 7,872 | 16 | 125,952 | 15.38 GiB |
| `replica-5` | 5 | 7,872 | 16 | 125,952 | 15.38 GiB |
| **fleet** | | **47,232** | | **755,712** | **92.25 GiB** |

Every replica reports the same figure. At 128 KiB per token, that is 15.38 GiB of KV per replica.

### Against the figure computed by hand

| | per replica | fleet |
|---|---:|---:|
| Measured | 125,952 tokens | 755,712 tokens |
| Hand-computed | 114,688 tokens | 688,128 tokens |
| Gap | +9.8% | +9.8% |

The arithmetic is 32 layers x 8 KV heads x 128 dim x 2 bytes x 2 (K,V) = 128 KiB per token. That part is exact. The soft input is the KV budget: the hand figure assumed 14.00 GiB was left for KV after weights, activations and CUDA graphs, and the engine actually left 15.38 GiB — the whole gap is that assumption, not the per-token arithmetic.

### Working set ratios rescaled off the measured total

| WS | offered session tokens | sessions of 2k |
|---:|---:|---:|
| 0.25 | 188,928 | 92 |
| 1 | 755,712 | 369 |
| 3 | 2,267,136 | 1,107 |
| 8 | 6,045,696 | 2,952 |

## Host GPU topology

| node | GPUs | threads on the node | threads per GPU |
|---:|---|---:|---:|
| 0 | [0 1 2 3] | 48 | 12 |
| 1 | [4 5] | 48 | 24 |

```
	GPU0	GPU1	GPU2	GPU3	GPU4	GPU5	CPU Affinity	NUMA Affinity	GPU NUMA ID
GPU0	 X 	PIX	NODE	NODE	SYS	SYS	0-23,48-71	0		N/A
GPU1	PIX	 X 	NODE	NODE	SYS	SYS	0-23,48-71	0		N/A
GPU2	NODE	NODE	 X 	PIX	SYS	SYS	0-23,48-71	0		N/A
GPU3	NODE	NODE	PIX	 X 	SYS	SYS	0-23,48-71	0		N/A
GPU4	SYS	SYS	SYS	SYS	 X 	PIX	24-47,72-95	1		N/A
GPU5	SYS	SYS	SYS	SYS	PIX	 X 	24-47,72-95	1		N/A

Legend:

  X    = Self
  SYS  = Connection traversing PCIe as well as the SMP interconnect between NUMA nodes (e.g., QPI/UPI)
  NODE = Connection traversing PCIe as well as the interconnect between PCIe Host Bridges within a NUMA node
  PHB  = Connection traversing PCIe as well as a PCIe Host Bridge (typically the CPU)
  PXB  = Connection traversing multiple PCIe bridges (without traversing the PCIe Host Bridge)
  PIX  = Connection traversing at most a single PCIe bridge
  NV#  = Connection traversing a bonded set of # NVLinks
```

## Hardware latency floor, and the SLO derived from it

One request at a time, straight at each replica, 1487 successful requests pooled across 6 replicas.

1.3% of the prompt tokens behind this figure came out of the replicas' prefix caches, against a 5% limit — so this is the cost of prefilling, not of finding a prompt already there.

| | p50 | p95 | p99 |
|---|---:|---:|---:|
| TTFT | 329ms | 341ms | 346ms |
| Inter-token | 8ms | 8ms | — |

**SLO: TTFT < 990ms, inter-token p50 < 24ms (3x the measured floor of 329ms and 7.8ms)**

The multiple is a judgement and the floor is not, so the alternatives are published beside it:

| multiple | TTFT | inter-token p50 |
|---:|---:|---:|
| 2x | 660ms | 16ms |
| 3x | 990ms | 24ms ← chosen |
| 4x | 1.32s | 32ms |
| 5x | 1.65s | 39ms |

## Replica symmetry

**Interchangeable within the 8% tolerance at concurrency 1**, which settled it. **Unresolved at concurrency 32**, where the measurement's own noise is wider than the tolerance — neither a difference between replicas nor evidence that there is none, and no escalation follows from it.

- at concurrency 32, TTFT p50 spread 22.1% over the 8.0% tolerance: replica-5 at 1.292s against replica-4 at 1.058s
- at concurrency 32 that spread is not resolvable: one replica varies 36.3% against itself between repetitions, wider than the 8.0% tolerance, so the difference between replicas is inside the measurement's own noise. Lengthen the probe or add repetitions rather than acting on it

### Concurrency 1

| replica | GPU | NUMA | reqs | TTFT p50 | TTFT p95 | ITL p50 | tput/s | batch mean/max | queued max | prefix hits |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `replica-0` | 0 | 0 | 245 | 332ms | 343ms | 8ms | 0.20 | — | — | 1.3% |
| `replica-1` | 1 | 0 | 247 | 329ms | 338ms | 8ms | 0.20 | — | — | 1.3% |
| `replica-2` | 2 | 0 | 251 | 324ms | 334ms | 8ms | 0.21 | — | — | 1.3% |
| `replica-3` | 3 | 0 | 251 | 330ms | 345ms | 8ms | 0.21 | — | — | 1.3% |
| `replica-4` | 4 | 1 | 246 | 331ms | 341ms | 8ms | 0.20 | — | — | 1.3% |
| `replica-5` | 5 | 1 | 247 | 327ms | 337ms | 8ms | 0.20 | — | — | 1.3% |

TTFT p50 spread **2.3%** (replica-0 slowest, replica-2 fastest), inter-token spread **1.3%**, between NUMA nodes **0.0%** — inside the 8% tolerance.

| NUMA node | replicas | threads per GPU | mean TTFT p50 | mean ITL p50 |
|---:|---:|---:|---:|---:|
| 0 | 4 | 12 | 329ms | 8ms |
| 1 | 2 | 24 | 329ms | 8ms |

Between nodes **0.0%** — inside the 8% tolerance. A node's own mean moves 1.0% between repetitions, so this settles it.

A replica varies 2.8% against itself between repetitions — inside the 8% tolerance — so this rules an over-tolerance difference out even where it cannot resolve the 2.3% it sees.

### Concurrency 32

| replica | GPU | NUMA | reqs | TTFT p50 | TTFT p95 | ITL p50 | tput/s | batch mean/max | queued max | prefix hits |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `replica-0` | 0 | 0 | 529 | 1137ms | 2029ms | 51ms | 0.43 | — | — | 1.3% |
| `replica-1` | 1 | 0 | 546 | 1172ms | 2001ms | 52ms | 0.44 | — | — | 1.3% |
| `replica-2` | 2 | 0 | 549 | 1137ms | 2009ms | 51ms | 0.45 | — | — | 1.3% |
| `replica-3` | 3 | 0 | 534 | 1236ms | 2019ms | 50ms | 0.43 | — | — | 1.3% |
| `replica-4` | 4 | 1 | 533 | 1058ms | 1949ms | 53ms | 0.43 | — | — | 1.3% |
| `replica-5` | 5 | 1 | 543 | 1292ms | 2027ms | 51ms | 0.44 | — | — | 1.3% |

TTFT p50 spread **22.1%** (replica-5 slowest, replica-4 fastest), inter-token spread **5.4%**, between NUMA nodes **0.4%** — **outside the 8% tolerance**.

| NUMA node | replicas | threads per GPU | mean TTFT p50 | mean ITL p50 |
|---:|---:|---:|---:|---:|
| 0 | 4 | 12 | 1171ms | 51ms |
| 1 | 2 | 24 | 1175ms | 52ms |

Between nodes **0.4%** — inside the 8% tolerance. **Not resolvable**: a node's own mean moves 8.2% between repetitions, wider than the 8% tolerance and more than the gap between nodes.

**Not resolvable at this sample size**: one replica varies 36.3% against *itself* between repetitions, wider than the 8% tolerance and more than the 22.1% between replicas. Lengthen the probe or add repetitions rather than acting on it.

