# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 11.68 | 13.04 | 418ms | 1690ms | 13ms | 3274 | 3274 | 0 | 0 | 342 | yes |  |
| closed-loop | 32 users | 2 | 11.53 | 12.97 | 419ms | 1781ms | 13ms | 3265 | 3265 | 0 | 0 | 361 | yes |  |
| closed-loop | 32 users | 3 | 11.13 | 12.69 | 421ms | 1928ms | 13ms | 3186 | 3186 | 0 | 0 | 391 | yes |  |
