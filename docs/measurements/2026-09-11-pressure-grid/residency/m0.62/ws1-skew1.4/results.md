# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 21.62 | 23.82 | 115ms | 842ms | 13ms | 5975 | 5975 | 0 | 0 | 552 | yes |  |
| closed-loop | 32 users | 2 | 20.47 | 24.53 | 108ms | 756ms | 13ms | 6165 | 6165 | 0 | 0 | 1020 | yes |  |
| closed-loop | 32 users | 3 | 5.66 | 13.93 | 200ms | 1035ms | 31ms | 3498 | 3498 | 0 | 0 | 2077 | yes |  |
