# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 14.76 | 15.94 | 382ms | 1715ms | 12ms | 4005 | 4005 | 0 | 0 | 297 | yes |  |
| closed-loop | 32 users | 2 | 15.32 | 16.39 | 373ms | 1648ms | 12ms | 4119 | 4119 | 0 | 0 | 268 | yes |  |
| closed-loop | 32 users | 3 | 14.86 | 16.05 | 385ms | 1665ms | 12ms | 4031 | 4031 | 0 | 0 | 300 | yes |  |
