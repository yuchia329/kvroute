# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 11.42 | 12.77 | 422ms | 1711ms | 13ms | 3215 | 3215 | 0 | 0 | 340 | yes |  |
| closed-loop | 32 users | 2 | 11.19 | 12.65 | 421ms | 1893ms | 13ms | 3181 | 3181 | 0 | 0 | 366 | yes |  |
| closed-loop | 32 users | 3 | 11.37 | 12.83 | 422ms | 1932ms | 13ms | 3225 | 3225 | 0 | 0 | 368 | yes |  |
