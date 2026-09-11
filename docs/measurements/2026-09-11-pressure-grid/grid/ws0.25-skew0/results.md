# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 11.23 | 12.92 | 412ms | 1786ms | 12ms | 3248 | 3248 | 0 | 0 | 425 | yes |  |
| closed-loop | 32 users | 2 | 11.38 | 12.91 | 417ms | 1755ms | 12ms | 3247 | 3247 | 0 | 0 | 384 | yes |  |
| closed-loop | 32 users | 3 | 10.99 | 12.83 | 424ms | 1913ms | 12ms | 3220 | 3220 | 0 | 0 | 461 | yes |  |
