# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 17.48 | 18.38 | 130ms | 1180ms | 16ms | 4603 | 4603 | 0 | 0 | 227 | yes |  |
| closed-loop | 32 users | 2 | 24.89 | 25.18 | 101ms | 1004ms | 12ms | 6263 | 6263 | 0 | 0 | 72 | yes |  |
| closed-loop | 32 users | 3 | 21.36 | 21.71 | 113ms | 1067ms | 14ms | 5451 | 5451 | 0 | 0 | 86 | yes |  |
