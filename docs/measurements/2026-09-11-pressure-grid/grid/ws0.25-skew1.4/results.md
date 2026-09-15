# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 24.33 | 25.00 | 93ms | 1377ms | 12ms | 6268 | 6268 | 0 | 0 | 167 | yes |  |
| closed-loop | 32 users | 2 | 25.35 | 25.89 | 91ms | 1289ms | 12ms | 6516 | 6516 | 0 | 0 | 135 | yes |  |
| closed-loop | 32 users | 3 | 26.02 | 26.53 | 91ms | 1116ms | 12ms | 6658 | 6658 | 0 | 0 | 126 | yes |  |
