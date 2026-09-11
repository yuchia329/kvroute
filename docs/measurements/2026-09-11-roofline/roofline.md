# Roofline: NVIDIA GeForce RTX 3090

Each point is every engine step of one shape, pooled: the FLOPs and bytes the model's shapes say those steps had to do, over the GPU time Nsight Systems measured them running. The work is counted rather than read off performance counters, which this driver reserves for root, and it is the least the step could have done — so a point's intensity is the model's, and only its height is the engine's.

## Ceilings

| | memory bandwidth | fp16 tensor compute | ridge |
|---|---:|---:|---:|
| measured on this card | 844 GB/s | 62.0 TFLOP/s | 73.5 FLOP/byte |
| datasheet | 936 GB/s | 71.0 TFLOP/s | 75.9 FLOP/byte |

## Steps

| point | steps | FLOP/byte | TFLOP/s | GB/s | bound | of the measured roof |
|---|---:|---:|---:|---:|---|---:|
| decode ×1 at ~256 tokens | 64 | 3.2 | 2.5 | 780 | bandwidth | 92% |
| decode ×2 at ~256 tokens | 62 | 6.4 | 4.9 | 768 | bandwidth | 91% |
| decode ×4 at ~256 tokens | 62 | 12.5 | 9.4 | 755 | bandwidth | 89% |
| decode ×8 at ~256 tokens | 64 | 24.2 | 17.8 | 737 | bandwidth | 87% |
| decode ×16 at ~256 tokens | 62 | 45.3 | 30.2 | 667 | bandwidth | 79% |
| decode ×32 at ~256 tokens | 60 | 80.3 | 39.9 | 497 | compute | 64% |
| decode ×64 at ~256 tokens | 57 | 131 | 44.9 | 343 | compute | 72% |
| decode ×128 at ~256 tokens | 46 | 192 | 49.5 | 258 | compute | 80% |
| decode ×256 at ~256 tokens | 29 | 249 | 51.2 | 205 | compute | 83% |
| decode ×1 at ~2048 tokens | 66 | 3.2 | 2.5 | 763 | bandwidth | 90% |
| decode ×4 at ~2048 tokens | 61 | 11.1 | 8.2 | 738 | bandwidth | 87% |
| decode ×16 at ~2048 tokens | 48 | 28.3 | 19.0 | 671 | bandwidth | 79% |
| decode ×48 at ~2048 tokens | 15 | 42.9 | 23.7 | 551 | bandwidth | 65% |
| prefill 128 tokens | 3 | 339 | 59.2 | 174 | compute | 95% |
| prefill 256 tokens | 10 | 610 | 61.7 | 101 | compute | 99% |
| prefill 512 tokens | 3 | 1017 | 62.0 | 61 | compute | 100% |
| prefill 1024 tokens | 3 | 1532 | 61.9 | 40 | compute | 100% |
| prefill 2048 tokens | 13 | 2070 | 62.1 | 30 | compute | 100% |
| prefill 2048 tokens over 4096 | 6 | 2183 | 60.8 | 28 | compute | 98% |
| prefill 2048 tokens over 6144 | 3 | 2291 | 61.1 | 27 | compute | 98% |
| prefill 1856 tokens over 8000 | 3 | 2303 | 60.0 | 26 | compute | 97% |

261 steps are not drawn: prefill and decode in one step, prompts prefilled together, or a decode batch's last few steps after one sequence finished early.
