# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 19.44 | 20.31 | 110ms | 1403ms | 12ms | 5099 | 5099 | 0 | 0 | 217 | yes |  |
| closed-loop | 32 users | 2 | 19.03 | 19.85 | 108ms | 1417ms | 12ms | 4984 | 4984 | 0 | 0 | 207 | yes |  |
| closed-loop | 32 users | 3 | 19.45 | 20.30 | 110ms | 1422ms | 12ms | 5092 | 5092 | 0 | 0 | 212 | yes |  |
