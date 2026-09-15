# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 9.48 | 11.25 | 427ms | 4249ms | 16ms | 2825 | 2825 | 0 | 0 | 444 | yes |  |
| closed-loop | 32 users | 2 | 12.81 | 14.21 | 380ms | 2356ms | 13ms | 3560 | 3560 | 0 | 0 | 351 | yes |  |
| closed-loop | 32 users | 3 | 8.80 | 10.68 | 414ms | 2109ms | 16ms | 2684 | 2684 | 0 | 0 | 471 | yes |  |
