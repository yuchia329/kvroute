# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 12.28 | 13.41 | 403ms | 1781ms | 12ms | 3372 | 3372 | 0 | 0 | 283 | yes |  |
| closed-loop | 32 users | 2 | 11.78 | 12.91 | 410ms | 1800ms | 13ms | 3241 | 3241 | 0 | 0 | 282 | yes |  |
| closed-loop | 32 users | 3 | 11.49 | 12.92 | 414ms | 2025ms | 13ms | 3262 | 3262 | 0 | 0 | 362 | yes |  |
