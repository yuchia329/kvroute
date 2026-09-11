# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 11.71 | 13.53 | 414ms | 1923ms | 12ms | 3408 | 3408 | 0 | 0 | 458 | yes |  |
| closed-loop | 32 users | 2 | 11.76 | 13.40 | 408ms | 1711ms | 12ms | 3370 | 3370 | 0 | 0 | 411 | yes |  |
| closed-loop | 32 users | 3 | 11.94 | 13.64 | 407ms | 1759ms | 12ms | 3429 | 3429 | 0 | 0 | 428 | yes |  |
