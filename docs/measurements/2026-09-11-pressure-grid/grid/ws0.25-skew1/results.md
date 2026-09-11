# Concurrency sweep — session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 24.61 | 28.32 | 94ms | 435ms | 14ms | 7097 | 7097 | 0 | 0 | 929 | yes |  |
| closed-loop | 32 users | 2 | 10.42 | 19.76 | 109ms | 482ms | 12ms | 4967 | 4967 | 0 | 0 | 2348 | yes | **1** |
| closed-loop | 32 users | 3 | 8.83 | 17.90 | 113ms | 558ms | 26ms | 4505 | 4505 | 0 | 0 | 2282 | yes |  |

Flagged cells — these are not averaged in without being read first:

- `session_affinity-c32-r2`: still warming up: the first half of the measured window was 43% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
