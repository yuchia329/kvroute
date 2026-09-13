# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 28.22 | 28.32 | 97ms | 744ms | 12ms | 7104 | 7104 | 0 | 0 | 25 | yes |  |
| closed-loop | 32 users | 2 | 7.19 | 12.90 | 188ms | 1737ms | 20ms | 3237 | 3237 | 0 | 0 | 1432 | yes |  |
| closed-loop | 32 users | 3 | 23.68 | 24.08 | 104ms | 1108ms | 13ms | 6036 | 6036 | 0 | 0 | 101 | yes |  |
