# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 6.35 | 8.89 | 414ms | 1768ms | 10ms | 2239 | 2239 | 0 | 0 | 640 | yes |  |
| closed-loop | 32 users | 2 | 6.51 | 9.04 | 412ms | 1945ms | 10ms | 2285 | 2285 | 0 | 0 | 641 | yes |  |
| closed-loop | 32 users | 3 | 6.56 | 9.12 | 414ms | 1705ms | 10ms | 2292 | 2292 | 0 | 0 | 643 | yes |  |
