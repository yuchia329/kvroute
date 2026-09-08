# Characterization — 2026-09-07

Model `hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4`, workload `fixed(prompt=2048B,output=64t,seed=2)`, 6 replicas **driven all at once**, so each one's host-side work competed with the others', closed-loop driver.

> ⚠️ **This characterization is flagged.** Every downstream figure scales off these
> numbers, so read the reasons before using any of them.

> - replica symmetry is unresolved at one or more levels: at concurrency 1, TTFT p50 spread 12.7% over the 8.0% tolerance: replica-3 at 380ms against replica-5 at 337ms; at concurrency 1 that spread is not resolvable: one replica varies 21.1% against itself between repetitions, wider than the 8.0% tolerance, so the difference between replicas is inside the measurement's own noise. Lengthen the probe or add repetitions rather than acting on it

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

One request at a time, straight at each replica, 1013 successful requests pooled across 6 replicas.

1.3% of the prompt tokens behind this figure came out of the replicas' prefix caches, against a 5% limit — so this is the cost of prefilling, not of finding a prompt already there.

| | p50 | p95 | p99 |
|---|---:|---:|---:|
| TTFT | 340ms | 392ms | 409ms |
| Inter-token | 8ms | 8ms | — |

**SLO: TTFT < 1.03s, inter-token p50 < 24ms (3x the measured floor of 340ms and 7.9ms)**

The multiple is a judgement and the floor is not, so the alternatives are published beside it:

| multiple | TTFT | inter-token p50 |
|---:|---:|---:|
| 2x | 690ms | 16ms |
| 3x | 1.03s | 24ms ← chosen |
| 4x | 1.37s | 32ms |
| 5x | 1.71s | 40ms |

## Replica symmetry

**Nothing was settled.** No level could tell a difference from its own noise at the 8% tolerance, so this is neither evidence that the replicas differ nor evidence that they do not.

- at concurrency 1, TTFT p50 spread 12.7% over the 8.0% tolerance: replica-3 at 380ms against replica-5 at 337ms
- at concurrency 1 that spread is not resolvable: one replica varies 21.1% against itself between repetitions, wider than the 8.0% tolerance, so the difference between replicas is inside the measurement's own noise. Lengthen the probe or add repetitions rather than acting on it

### Concurrency 1

| replica | GPU | NUMA | reqs | TTFT p50 | TTFT p95 | ITL p50 | tput/s | batch mean/max | queued max | prefix hits |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `replica-0` | 0 | 0 | 165 | 353ms | 374ms | 8ms | 0.93 | 0.6 / 1 | 0 | 1.3% |
| `replica-1` | 1 | 0 | 171 | 338ms | 352ms | 8ms | 0.97 | 0.6 / 1 | 0 | 1.3% |
| `replica-2` | 2 | 0 | 170 | 340ms | 372ms | 8ms | 0.96 | 0.5 / 1 | 0 | 1.3% |
| `replica-3` | 3 | 0 | 163 | 380ms | 411ms | 8ms | 0.92 | 0.6 / 1 | 0 | 1.3% |
| `replica-4` | 4 | 1 | 173 | 339ms | 351ms | 8ms | 0.98 | 0.6 / 1 | 0 | 1.3% |
| `replica-5` | 5 | 1 | 171 | 337ms | 348ms | 8ms | 0.97 | 0.6 / 1 | 0 | 1.3% |

TTFT p50 spread **12.7%** (replica-3 slowest, replica-5 fastest), inter-token spread **3.8%**, between NUMA nodes **4.3%** — **outside the 8% tolerance**.

| NUMA node | replicas | threads per GPU | mean TTFT p50 | mean ITL p50 |
|---:|---:|---:|---:|---:|
| 0 | 4 | 12 | 352ms | 8ms |
| 1 | 2 | 24 | 338ms | 8ms |

Between nodes **4.3%** — inside the 8% tolerance. **Not resolvable**: a node's own mean moves 11.9% between repetitions, wider than the 8% tolerance and more than the gap between nodes.

**Not resolvable at this sample size**: one replica varies 21.1% against *itself* between repetitions, wider than the 8% tolerance and more than the 12.7% between replicas. Lengthen the probe or add repetitions rather than acting on it.

