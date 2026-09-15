# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 27.38 | 27.70 | 95ms | 1002ms | 13ms | 6946 | 6946 | 0 | 0 | 79 | yes |  |
| closed-loop | 32 users | 2 | 29.03 | 29.25 | 93ms | 798ms | 12ms | 7340 | 7340 | 0 | 0 | 54 | yes |  |
| closed-loop | 32 users | 3 | 28.45 | 28.61 | 93ms | 777ms | 13ms | 7172 | 7172 | 0 | 0 | 41 | yes |  |
