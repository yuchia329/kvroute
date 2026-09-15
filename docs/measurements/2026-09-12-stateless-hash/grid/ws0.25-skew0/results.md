# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 34.34 | 34.51 | 85ms | 670ms | 12ms | 8653 | 8653 | 0 | 0 | 41 | yes |  |
| closed-loop | 32 users | 2 | 34.86 | 35.00 | 85ms | 538ms | 12ms | 8782 | 8782 | 0 | 0 | 34 | yes |  |
| closed-loop | 32 users | 3 | 33.75 | 33.99 | 87ms | 751ms | 12ms | 8551 | 8551 | 0 | 0 | 60 | yes |  |
