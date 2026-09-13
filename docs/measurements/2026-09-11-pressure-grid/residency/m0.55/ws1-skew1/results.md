# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 12.37 | 14.44 | 328ms | 1778ms | 16ms | 3626 | 3626 | 0 | 0 | 522 | yes |  |
| closed-loop | 32 users | 2 | 15.81 | 16.59 | 151ms | 1308ms | 15ms | 4161 | 4161 | 0 | 0 | 196 | yes | **1** |
| closed-loop | 32 users | 3 | 9.76 | 12.14 | 340ms | 4210ms | 15ms | 3047 | 3047 | 0 | 0 | 598 | yes | **1** |

Flagged cells — these are not averaged in without being read first:

- `prefix_affinity-c32-r2`: still warming up: the first half of the measured window was 192% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
- `prefix_affinity-c32-r3`: still warming up: the first half of the measured window was 48% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
