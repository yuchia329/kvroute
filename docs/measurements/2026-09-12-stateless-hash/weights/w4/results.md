# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 28.09 | 28.29 | 91ms | 803ms | 13ms | 7094 | 7094 | 0 | 0 | 50 | yes |  |
| closed-loop | 32 users | 2 | 28.90 | 29.15 | 92ms | 880ms | 12ms | 7312 | 7312 | 0 | 0 | 63 | yes |  |
| closed-loop | 32 users | 3 | 28.29 | 28.50 | 92ms | 812ms | 13ms | 7148 | 7148 | 0 | 0 | 52 | yes |  |
