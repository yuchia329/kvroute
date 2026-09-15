# KV transfer against prefill

idea.md §8's arithmetic, rebuilt by `cmd/disagg` from the rows beside it: what a request's KV cache would cost to move from the card that prefilled it to another, set against that prefill. Nothing here moved KV; the copies moved bytes of the same size.

Measured on nlp-gpu-01.be.ucsc.edu — driver 595.84, torch 2.13.0+cu130, CUDA 13.0 — from 2026-09-11T21:54:50.149368+00:00, in 2 passes, one per NUMA node the host buffer was bound to.

## A token's KV

2 (K and V) × 32 layers × 8 KV heads × 128 head dim × 2 bytes (float16) = **131,072 bytes**, 128 KiB a token, read off the model's own config.json. The engine keeps its cache in the model's dtype (`kv_cache_dtype=auto`).

## Peer access

Torch reported direct peer access for **no pair** of the 30 ordered pairs checked, so every card-to-card copy here went through host memory.

## The links the copies ran on

Sampled through NVML while every group of copies ran, the lowest link either card showed was **gen 3 x16**. Idle, the same cards report gen 1; see `pcie-tree.txt`.

In the *busy* condition, each card's own memory-bound copy loop ran at **406.3 GB/s** or more throughout, so those cards really were busy. That loop stays inside the card's own memory: it loads the card, not the link.

## Card and host memory

Each card's own link, the leg every card-to-card copy below is built from. These have no pair to class, so *local* is a host buffer on the card's own NUMA node and *remote* one on the other node. GB/s are decimal: 10⁹ bytes a second.

| condition | path | class | size | transfers | typical GB/s | slowest GB/s | slowest on | link |
|---|---|---|---:|---:|---:|---:|---|---|
| alone | h2d | local | 32 MiB | 60 | 7.89 | 7.83 | card 0, node 0 | gen 3 x16 |
| alone | h2d | local | 64 MiB | 60 | 7.88 | 7.85 | card 0, node 0 | gen 3 x16 |
| alone | h2d | local | 128 MiB | 60 | 7.88 | 7.86 | card 0, node 0 | gen 3 x16 |
| alone | h2d | local | 256 MiB | 60 | 7.88 | 7.87 | card 0, node 0 | gen 3 x16 |
| alone | h2d | local | 512 MiB | 60 | 7.87 | 7.87 | card 0, node 0 | gen 3 x16 |
| alone | h2d | local | 992 MiB | 60 | 7.87 | 7.87 | card 3, node 0 | gen 3 x16 |
| alone | h2d | remote | 32 MiB | 60 | 7.66 | 7.58 | card 4, node 0 | gen 3 x16 |
| alone | h2d | remote | 64 MiB | 60 | 7.68 | 7.61 | card 4, node 0 | gen 3 x16 |
| alone | h2d | remote | 128 MiB | 60 | 7.68 | 7.64 | card 4, node 0 | gen 3 x16 |
| alone | h2d | remote | 256 MiB | 60 | 7.68 | 7.64 | card 4, node 0 | gen 3 x16 |
| alone | h2d | remote | 512 MiB | 60 | 7.69 | 7.65 | card 4, node 0 | gen 3 x16 |
| alone | h2d | remote | 992 MiB | 60 | 7.68 | 7.65 | card 5, node 0 | gen 3 x16 |
| alone | d2h | local | 32 MiB | 60 | 8.44 | 8.42 | card 4, node 1 | gen 3 x16 |
| alone | d2h | local | 64 MiB | 60 | 8.48 | 8.47 | card 4, node 1 | gen 3 x16 |
| alone | d2h | local | 128 MiB | 60 | 8.49 | 8.48 | card 2, node 0 | gen 3 x16 |
| alone | d2h | local | 256 MiB | 60 | 8.49 | 8.48 | card 3, node 0 | gen 3 x16 |
| alone | d2h | local | 512 MiB | 60 | 8.50 | 8.49 | card 3, node 0 | gen 3 x16 |
| alone | d2h | local | 992 MiB | 60 | 8.50 | 8.49 | card 2, node 0 | gen 3 x16 |
| alone | d2h | remote | 32 MiB | 60 | 7.67 | 7.66 | card 2, node 1 | gen 3 x16 |
| alone | d2h | remote | 64 MiB | 60 | 7.70 | 7.69 | card 3, node 1 | gen 3 x16 |
| alone | d2h | remote | 128 MiB | 60 | 7.71 | 7.71 | card 4, node 0 | gen 3 x16 |
| alone | d2h | remote | 256 MiB | 60 | 7.72 | 7.71 | card 2, node 1 | gen 3 x16 |
| alone | d2h | remote | 512 MiB | 60 | 7.72 | 7.72 | card 3, node 1 | gen 3 x16 |
| alone | d2h | remote | 992 MiB | 60 | 7.72 | 7.72 | card 3, node 1 | gen 3 x16 |

## Card to card

*peer* is CUDA's own copy, which the driver stages through host memory when peer access is off; *bounce* stages it by hand through pinned host memory, chunked and pipelined. The slowest pair is the median of the slowest (pair, host node) in its class. GB/s are decimal: 10⁹ bytes a second.

| condition | path | class | size | transfers | typical GB/s | slowest GB/s | slowest on | link |
|---|---|---|---:|---:|---:|---:|---|---|
| alone | peer | PIX | 32 MiB | 120 | 3.69 | 3.49 | 5→4, node 0 | gen 3 x16 |
| alone | peer | PIX | 64 MiB | 120 | 3.69 | 3.51 | 4→5, node 0 | gen 3 x16 |
| alone | peer | PIX | 128 MiB | 120 | 3.70 | 3.50 | 5→4, node 0 | gen 3 x16 |
| alone | peer | PIX | 256 MiB | 120 | 3.71 | 3.52 | 5→4, node 0 | gen 3 x16 |
| alone | peer | PIX | 512 MiB | 120 | 3.71 | 3.51 | 5→4, node 0 | gen 3 x16 |
| alone | peer | PIX | 992 MiB | 120 | 3.70 | 3.50 | 5→4, node 0 | gen 3 x16 |
| alone | peer | NODE | 32 MiB | 160 | 3.72 | 3.56 | 3→0, node 1 | gen 3 x16 |
| alone | peer | NODE | 64 MiB | 160 | 3.73 | 3.56 | 1→3, node 1 | gen 3 x16 |
| alone | peer | NODE | 128 MiB | 160 | 3.74 | 3.57 | 2→1, node 1 | gen 3 x16 |
| alone | peer | NODE | 256 MiB | 160 | 3.75 | 3.57 | 2→0, node 1 | gen 3 x16 |
| alone | peer | NODE | 512 MiB | 160 | 3.75 | 3.58 | 3→0, node 1 | gen 3 x16 |
| alone | peer | NODE | 992 MiB | 160 | 3.75 | 3.60 | 2→0, node 1 | gen 3 x16 |
| alone | peer | SYS | 32 MiB | 320 | 3.65 | 3.61 | 3→5, node 1 | gen 3 x16 |
| alone | peer | SYS | 64 MiB | 320 | 3.67 | 3.60 | 2→5, node 1 | gen 3 x16 |
| alone | peer | SYS | 128 MiB | 320 | 3.67 | 3.63 | 3→5, node 1 | gen 3 x16 |
| alone | peer | SYS | 256 MiB | 320 | 3.68 | 3.63 | 2→5, node 1 | gen 3 x16 |
| alone | peer | SYS | 512 MiB | 320 | 3.68 | 3.64 | 2→5, node 1 | gen 3 x16 |
| alone | peer | SYS | 992 MiB | 320 | 3.67 | 3.62 | 4→0, node 0 | gen 3 x16 |
| alone | bounce | PIX | 32 MiB | 120 | 6.10 | 5.84 | 2→3, node 1 | gen 3 x16 |
| alone | bounce | PIX | 64 MiB | 120 | 6.74 | 6.45 | 4→5, node 0 | gen 3 x16 |
| alone | bounce | PIX | 128 MiB | 120 | 7.12 | 6.81 | 4→5, node 0 | gen 3 x16 |
| alone | bounce | PIX | 256 MiB | 120 | 7.32 | 7.02 | 4→5, node 0 | gen 3 x16 |
| alone | bounce | PIX | 512 MiB | 120 | 7.42 | 7.12 | 4→5, node 0 | gen 3 x16 |
| alone | bounce | PIX | 992 MiB | 120 | 7.48 | 7.17 | 4→5, node 0 | gen 3 x16 |
| alone | bounce | NODE | 32 MiB | 160 | 6.26 | 6.08 | 2→0, node 1 | gen 3 x16 |
| alone | bounce | NODE | 64 MiB | 160 | 6.94 | 6.77 | 2→0, node 1 | gen 3 x16 |
| alone | bounce | NODE | 128 MiB | 160 | 7.34 | 7.18 | 2→1, node 1 | gen 3 x16 |
| alone | bounce | NODE | 256 MiB | 160 | 7.54 | 7.41 | 3→0, node 1 | gen 3 x16 |
| alone | bounce | NODE | 512 MiB | 160 | 7.64 | 7.53 | 3→1, node 1 | gen 3 x16 |
| alone | bounce | NODE | 992 MiB | 160 | 7.69 | 7.58 | 3→1, node 1 | gen 3 x16 |
| alone | bounce | SYS | 32 MiB | 320 | 6.13 | 6.11 | 5→3, node 0 | gen 3 x16 |
| alone | bounce | SYS | 64 MiB | 320 | 6.81 | 6.79 | 2→5, node 0 | gen 3 x16 |
| alone | bounce | SYS | 128 MiB | 320 | 7.21 | 7.20 | 1→5, node 0 | gen 3 x16 |
| alone | bounce | SYS | 256 MiB | 320 | 7.43 | 7.42 | 2→4, node 0 | gen 3 x16 |
| alone | bounce | SYS | 512 MiB | 320 | 7.54 | 7.53 | 1→5, node 0 | gen 3 x16 |
| alone | bounce | SYS | 992 MiB | 320 | 7.60 | 7.59 | 3→5, node 0 | gen 3 x16 |
| busy | peer | PIX | 256 MiB | 120 | 3.64 | 3.45 | 5→4, node 0 | gen 3 x16 |
| busy | peer | NODE | 256 MiB | 160 | 3.68 | 3.51 | 3→0, node 1 | gen 3 x16 |
| busy | peer | SYS | 256 MiB | 320 | 3.61 | 3.56 | 3→5, node 1 | gen 3 x16 |
| busy | bounce | PIX | 256 MiB | 120 | 7.28 | 7.00 | 5→4, node 0 | gen 3 x16 |
| busy | bounce | NODE | 256 MiB | 160 | 7.50 | 7.37 | 2→0, node 1 | gen 3 x16 |
| busy | bounce | SYS | 256 MiB | 320 | 7.41 | 7.33 | 0→5, node 0 | gen 3 x16 |
| together | peer | PIX | 256 MiB | 120 | 3.71 | 3.54 | 5→4, node 0 | gen 3 x16 |
| together | peer | NODE | 256 MiB | 80 | 3.51 | 3.43 | 3→1, node 1 | gen 3 x16 |
| together | peer | SYS | 256 MiB | 80 | 3.52 | 3.49 | 5→1, node 0 | gen 3 x16 |
| together | bounce | PIX | 256 MiB | 120 | 6.92 | 6.85 | 1→0, node 1 | gen 3 x16 |
| together | bounce | NODE | 256 MiB | 80 | 4.16 | 4.07 | 2→0, node 1 | gen 3 x16 |
| together | bounce | SYS | 256 MiB | 80 | 4.16 | 4.07 | 0→4, node 0 | gen 3 x16 |

## Prefill

One replica, one request at a time, every prompt unseen. *engine prefill* is `vllm:request_prefill_time_seconds`: from the scheduler taking the request to its first token. A request the cache served any of, or the engine did not count exactly once, is excluded.

| tokens | requests | excluded | engine prefill | engine TTFT | client |
|---:|---:|---:|---:|---:|---:|
| 256 | 10 | 0 | 65.4 ms | 68.6 ms | 73.9 ms |
| 512 | 10 | 0 | 124.1 ms | 127.5 ms | 133.7 ms |
| 1,024 | 10 | 0 | 245.2 ms | 248.8 ms | 255.1 ms |
| 2,048 | 10 | 0 | 490.7 ms | 495.7 ms | 504.0 ms |
| 4,096 | 10 | 0 | 1025.3 ms | 1031.7 ms | 1044.7 ms |
| 7,936 | 10 | 0 | 2126.2 ms | 2136.1 ms | 2160.9 ms |

## Transfer against prefill

A request's KV moved once its prefill has finished, none of it overlapped: tokens × a token's KV, over the bandwidth measured at exactly that size.

| tokens | KV | prefill | condition | path | class | typical | slowest pair | slowest / prefill |
|---:|---:|---:|---|---|---|---:|---:|---:|
| 256 | 32 MiB | 65.4 ms | alone | peer | PIX | 9.1 ms | 9.6 ms | 14.7% |
| 256 | 32 MiB | 65.4 ms | alone | peer | NODE | 9.0 ms | 9.4 ms | 14.4% |
| 256 | 32 MiB | 65.4 ms | alone | peer | SYS | 9.2 ms | 9.3 ms | 14.2% |
| 256 | 32 MiB | 65.4 ms | alone | bounce | PIX | 5.5 ms | 5.7 ms | 8.8% |
| 256 | 32 MiB | 65.4 ms | alone | bounce | NODE | 5.4 ms | 5.5 ms | 8.4% |
| 256 | 32 MiB | 65.4 ms | alone | bounce | SYS | 5.5 ms | 5.5 ms | 8.4% |
| 512 | 64 MiB | 124.1 ms | alone | peer | PIX | 18.2 ms | 19.1 ms | 15.4% |
| 512 | 64 MiB | 124.1 ms | alone | peer | NODE | 18.0 ms | 18.8 ms | 15.2% |
| 512 | 64 MiB | 124.1 ms | alone | peer | SYS | 18.3 ms | 18.6 ms | 15.0% |
| 512 | 64 MiB | 124.1 ms | alone | bounce | PIX | 10.0 ms | 10.4 ms | 8.4% |
| 512 | 64 MiB | 124.1 ms | alone | bounce | NODE | 9.7 ms | 9.9 ms | 8.0% |
| 512 | 64 MiB | 124.1 ms | alone | bounce | SYS | 9.9 ms | 9.9 ms | 8.0% |
| 1,024 | 128 MiB | 245.2 ms | alone | peer | PIX | 36.2 ms | 38.3 ms | 15.6% |
| 1,024 | 128 MiB | 245.2 ms | alone | peer | NODE | 35.9 ms | 37.6 ms | 15.3% |
| 1,024 | 128 MiB | 245.2 ms | alone | peer | SYS | 36.6 ms | 37.0 ms | 15.1% |
| 1,024 | 128 MiB | 245.2 ms | alone | bounce | PIX | 18.8 ms | 19.7 ms | 8.0% |
| 1,024 | 128 MiB | 245.2 ms | alone | bounce | NODE | 18.3 ms | 18.7 ms | 7.6% |
| 1,024 | 128 MiB | 245.2 ms | alone | bounce | SYS | 18.6 ms | 18.6 ms | 7.6% |
| 2,048 | 256 MiB | 490.7 ms | alone | peer | PIX | 72.4 ms | 76.3 ms | 15.6% |
| 2,048 | 256 MiB | 490.7 ms | alone | peer | NODE | 71.5 ms | 75.1 ms | 15.3% |
| 2,048 | 256 MiB | 490.7 ms | alone | peer | SYS | 73.0 ms | 74.0 ms | 15.1% |
| 2,048 | 256 MiB | 490.7 ms | alone | bounce | PIX | 36.7 ms | 38.2 ms | 7.8% |
| 2,048 | 256 MiB | 490.7 ms | alone | bounce | NODE | 35.6 ms | 36.2 ms | 7.4% |
| 2,048 | 256 MiB | 490.7 ms | alone | bounce | SYS | 36.1 ms | 36.2 ms | 7.4% |
| 2,048 | 256 MiB | 490.7 ms | busy | peer | PIX | 73.7 ms | 77.8 ms | 15.9% |
| 2,048 | 256 MiB | 490.7 ms | busy | peer | NODE | 73.0 ms | 76.5 ms | 15.6% |
| 2,048 | 256 MiB | 490.7 ms | busy | peer | SYS | 74.4 ms | 75.3 ms | 15.3% |
| 2,048 | 256 MiB | 490.7 ms | busy | bounce | PIX | 36.9 ms | 38.3 ms | 7.8% |
| 2,048 | 256 MiB | 490.7 ms | busy | bounce | NODE | 35.8 ms | 36.4 ms | 7.4% |
| 2,048 | 256 MiB | 490.7 ms | busy | bounce | SYS | 36.2 ms | 36.6 ms | 7.5% |
| 2,048 | 256 MiB | 490.7 ms | together | peer | PIX | 72.4 ms | 75.9 ms | 15.5% |
| 2,048 | 256 MiB | 490.7 ms | together | peer | NODE | 76.6 ms | 78.3 ms | 16.0% |
| 2,048 | 256 MiB | 490.7 ms | together | peer | SYS | 76.2 ms | 76.8 ms | 15.7% |
| 2,048 | 256 MiB | 490.7 ms | together | bounce | PIX | 38.8 ms | 39.2 ms | 8.0% |
| 2,048 | 256 MiB | 490.7 ms | together | bounce | NODE | 64.6 ms | 66.0 ms | 13.4% |
| 2,048 | 256 MiB | 490.7 ms | together | bounce | SYS | 64.5 ms | 65.9 ms | 13.4% |
| 4,096 | 512 MiB | 1025.3 ms | alone | peer | PIX | 144.7 ms | 152.8 ms | 14.9% |
| 4,096 | 512 MiB | 1025.3 ms | alone | peer | NODE | 143.0 ms | 149.9 ms | 14.6% |
| 4,096 | 512 MiB | 1025.3 ms | alone | peer | SYS | 146.0 ms | 147.7 ms | 14.4% |
| 4,096 | 512 MiB | 1025.3 ms | alone | bounce | PIX | 72.3 ms | 75.5 ms | 7.4% |
| 4,096 | 512 MiB | 1025.3 ms | alone | bounce | NODE | 70.2 ms | 71.3 ms | 7.0% |
| 4,096 | 512 MiB | 1025.3 ms | alone | bounce | SYS | 71.2 ms | 71.3 ms | 7.0% |
| 7,936 | 992 MiB | 2126.2 ms | alone | peer | PIX | 280.8 ms | 297.0 ms | 14.0% |
| 7,936 | 992 MiB | 2126.2 ms | alone | peer | NODE | 277.1 ms | 289.1 ms | 13.6% |
| 7,936 | 992 MiB | 2126.2 ms | alone | peer | SYS | 283.7 ms | 287.0 ms | 13.5% |
| 7,936 | 992 MiB | 2126.2 ms | alone | bounce | PIX | 139.1 ms | 145.1 ms | 6.8% |
| 7,936 | 992 MiB | 2126.2 ms | alone | bounce | NODE | 135.2 ms | 137.2 ms | 6.5% |
| 7,936 | 992 MiB | 2126.2 ms | alone | bounce | SYS | 136.9 ms | 137.1 ms | 6.4% |

