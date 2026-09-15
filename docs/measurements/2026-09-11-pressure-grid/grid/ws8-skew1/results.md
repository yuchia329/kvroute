# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 11.05 | 12.67 | 411ms | 2052ms | 12ms | 3181 | 3181 | 0 | 0 | 407 | yes |  |
| closed-loop | 32 users | 2 | 11.20 | 12.72 | 415ms | 1851ms | 12ms | 3203 | 3203 | 0 | 0 | 383 | yes |  |
| closed-loop | 32 users | 3 | 10.81 | 12.65 | 425ms | 1827ms | 12ms | 3177 | 3177 | 0 | 0 | 463 | yes |  |
