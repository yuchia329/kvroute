# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 8.51 | 10.45 | 390ms | 7965ms | 16ms | 2625 | 2625 | 0 | 0 | 488 | yes | **1** |
| closed-loop | 32 users | 2 | 16.92 | 17.97 | 191ms | 1884ms | 13ms | 4496 | 4496 | 0 | 0 | 261 | yes | **1** |
| closed-loop | 32 users | 3 | 12.36 | 13.73 | 307ms | 4123ms | 15ms | 3439 | 3439 | 0 | 0 | 343 | yes | **1** |

Flagged cells — these are not averaged in without being read first:

- `prefix_affinity-c32-r1`: still warming up: the first half of the measured window was 55% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `prefix_affinity-c32-r2`: still warming up: the first half of the measured window was 132% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `prefix_affinity-c32-r3`: still warming up: the first half of the measured window was 202% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
