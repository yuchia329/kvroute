# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 17.67 | 18.90 | 137ms | 1417ms | 12ms | 4753 | 4753 | 0 | 0 | 309 | yes |  |
| closed-loop | 32 users | 2 | 15.94 | 17.79 | 186ms | 1216ms | 12ms | 4473 | 4473 | 0 | 0 | 464 | yes |  |
| closed-loop | 32 users | 3 | 16.21 | 17.88 | 240ms | 1372ms | 12ms | 4494 | 4494 | 0 | 0 | 422 | yes |  |
