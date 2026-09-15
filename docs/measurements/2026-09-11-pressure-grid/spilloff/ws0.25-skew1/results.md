# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 31.70 | 33.19 | 86ms | 409ms | 12ms | 8328 | 8328 | 0 | 0 | 374 | yes |  |
| closed-loop | 32 users | 2 | 31.56 | 32.31 | 88ms | 442ms | 12ms | 8105 | 8105 | 0 | 0 | 187 | yes |  |
| closed-loop | 32 users | 3 | 33.93 | 34.21 | 86ms | 420ms | 13ms | 8579 | 8579 | 0 | 0 | 70 | yes |  |
