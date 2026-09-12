# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 32.87 | 33.08 | 88ms | 712ms | 13ms | 8292 | 8292 | 0 | 0 | 51 | yes |  |
| closed-loop | 32 users | 2 | 31.99 | 32.14 | 88ms | 643ms | 13ms | 8064 | 8064 | 0 | 0 | 36 | yes |  |
| closed-loop | 32 users | 3 | 34.08 | 34.22 | 86ms | 532ms | 12ms | 8579 | 8579 | 0 | 0 | 35 | yes |  |
