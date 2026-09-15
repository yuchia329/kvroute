# Roofline: NVIDIA GeForce RTX 3090

Each point is every engine step of one shape, pooled: the FLOPs and bytes the model's shapes say those steps had to do, over the GPU time Nsight Systems measured them running. The work is counted rather than read off performance counters, which this driver reserves for root, and it is the least the step could have done — so a point's intensity is the model's, and only its height is the engine's.

## Ceilings

| | memory bandwidth | fp16 tensor compute | ridge |
|---|---:|---:|---:|
| measured on this card | 844 GB/s | 62.6 TFLOP/s | 74.2 FLOP/byte |
| datasheet | 936 GB/s | 71.0 TFLOP/s | 75.9 FLOP/byte |

## Steps

| point | steps | FLOP/byte | TFLOP/s | GB/s | bound | of the measured roof |
|---|---:|---:|---:|---:|---|---:|
| decode ×1 at ~256 tokens | 63 | 3.2 | 2.5 | 779 | bandwidth | 92% |
| decode ×2 at ~256 tokens | 63 | 6.4 | 4.9 | 769 | bandwidth | 91% |
| decode ×4 at ~256 tokens | 62 | 12.5 | 9.5 | 756 | bandwidth | 90% |
| decode ×8 at ~256 tokens | 64 | 24.2 | 17.8 | 737 | bandwidth | 87% |
| decode ×16 at ~256 tokens | 63 | 45.3 | 30.3 | 669 | bandwidth | 79% |
| decode ×32 at ~256 tokens | 60 | 80.3 | 39.8 | 495 | compute | 63% |
| decode ×64 at ~256 tokens | 56 | 131 | 44.9 | 342 | compute | 72% |
| decode ×128 at ~256 tokens | 46 | 192 | 49.4 | 258 | compute | 79% |
| decode ×256 at ~256 tokens | 29 | 249 | 50.9 | 204 | compute | 81% |
| decode ×1 at ~2048 tokens | 66 | 3.2 | 2.5 | 762 | bandwidth | 90% |
| decode ×4 at ~2048 tokens | 61 | 11.1 | 8.2 | 735 | bandwidth | 87% |
| decode ×16 at ~2048 tokens | 48 | 28.3 | 18.8 | 667 | bandwidth | 79% |
| decode ×48 at ~2048 tokens | 15 | 42.9 | 23.6 | 549 | bandwidth | 65% |
| prefill 128 tokens | 3 | 339 | 59.1 | 174 | compute | 94% |
| prefill 256 tokens | 10 | 610 | 61.5 | 101 | compute | 98% |
| prefill 512 tokens | 3 | 1017 | 62.2 | 61 | compute | 99% |
| prefill 1024 tokens | 3 | 1532 | 62.1 | 41 | compute | 99% |
| prefill 2048 tokens | 13 | 2070 | 61.8 | 30 | compute | 99% |
| prefill 2048 tokens over 4096 | 6 | 2183 | 60.9 | 28 | compute | 97% |
| prefill 2048 tokens over 6144 | 3 | 2291 | 60.9 | 27 | compute | 97% |
| prefill 1856 tokens over 8000 | 3 | 2303 | 60.0 | 26 | compute | 96% |

260 steps are not drawn: prefill and decode in one step, prompts prefilled together, or a decode batch's last few steps after one sequence finished early.
