# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 13.24 | 17.67 | 108ms | 1534ms | 14ms | 4432 | 4432 | 0 | 0 | 1112 | yes | **1** |
| closed-loop | 32 users | 2 | 27.99 | 29.12 | 88ms | 1141ms | 12ms | 7311 | 7311 | 0 | 0 | 282 | yes |  |
| closed-loop | 32 users | 3 | 11.57 | 15.39 | 97ms | 5297ms | 12ms | 3858 | 3858 | 0 | 0 | 957 | yes | **1** |

Flagged cells — these are not averaged in without being read first:

- `prefix_affinity-c32-r1`: still warming up: the first half of the measured window was 201% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `prefix_affinity-c32-r3`: still warming up: the first half of the measured window was 374% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
