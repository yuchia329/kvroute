# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 32.35 | 32.43 | 89ms | 641ms | 12ms | 8147 | 8147 | 0 | 0 | 21 | yes |  |
| closed-loop | 32 users | 2 | 32.93 | 33.03 | 88ms | 666ms | 13ms | 8293 | 8293 | 0 | 0 | 26 | yes |  |
| closed-loop | 32 users | 3 | 33.75 | 33.83 | 86ms | 586ms | 13ms | 8485 | 8485 | 0 | 0 | 21 | yes |  |
