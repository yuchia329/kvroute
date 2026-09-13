# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 28.50 | 28.60 | 98ms | 732ms | 12ms | 7171 | 7171 | 0 | 0 | 26 | yes |  |
| closed-loop | 32 users | 2 | 24.42 | 26.23 | 99ms | 800ms | 12ms | 6569 | 6569 | 0 | 0 | 454 | yes |  |
| closed-loop | 32 users | 3 | 24.56 | 24.74 | 106ms | 883ms | 14ms | 6202 | 6202 | 0 | 0 | 46 | yes |  |
