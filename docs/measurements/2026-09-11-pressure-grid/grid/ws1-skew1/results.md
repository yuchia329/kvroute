# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 13.31 | 14.74 | 391ms | 1747ms | 12ms | 3702 | 3702 | 0 | 0 | 359 | yes |  |
| closed-loop | 32 users | 2 | 13.74 | 14.99 | 387ms | 1642ms | 12ms | 3766 | 3766 | 0 | 0 | 313 | yes |  |
| closed-loop | 32 users | 3 | 13.49 | 14.79 | 393ms | 1709ms | 12ms | 3720 | 3720 | 0 | 0 | 328 | yes |  |
