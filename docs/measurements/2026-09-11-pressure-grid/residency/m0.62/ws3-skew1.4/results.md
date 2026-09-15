# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 29.10 | 29.14 | 95ms | 744ms | 12ms | 7317 | 7317 | 0 | 0 | 11 | yes |  |
| closed-loop | 32 users | 2 | 5.19 | 13.03 | 180ms | 1096ms | 30ms | 3269 | 3269 | 0 | 0 | 1967 | yes |  |
| closed-loop | 32 users | 3 | 14.38 | 19.90 | 142ms | 1020ms | 12ms | 5003 | 5003 | 0 | 0 | 1389 | yes |  |
