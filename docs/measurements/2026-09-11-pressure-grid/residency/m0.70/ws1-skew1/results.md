# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 5.39 | 8.07 | 419ms | 8873ms | 14ms | 2006 | 2006 | 0 | 0 | 667 | yes | **1** |
| closed-loop | 32 users | 2 | 7.00 | 9.26 | 410ms | 9659ms | 15ms | 2317 | 2317 | 0 | 0 | 565 | yes |  |
| closed-loop | 32 users | 3 | 2.91 | 6.05 | 785ms | 9084ms | 16ms | 1530 | 1530 | 0 | 0 | 794 | yes |  |

Flagged cells — these are not averaged in without being read first:

- `prefix_affinity-c32-r1`: still warming up: the first half of the measured window was 107% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
