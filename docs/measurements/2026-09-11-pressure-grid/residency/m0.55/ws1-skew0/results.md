# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.28 | 4.37 | 661ms | 15411ms | 11ms | 1134 | 1134 | 0 | 0 | 544 | yes |  |
| closed-loop | 32 users | 2 | 2.14 | 4.75 | 1013ms | 13753ms | 13ms | 1198 | 1198 | 0 | 0 | 658 | yes |  |
| closed-loop | 32 users | 3 | 2.70 | 5.31 | 749ms | 12126ms | 13ms | 1340 | 1340 | 0 | 0 | 659 | yes |  |
