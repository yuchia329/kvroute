# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 14.69 | 15.75 | 386ms | 1683ms | 13ms | 3954 | 3954 | 0 | 0 | 265 | yes |  |
| closed-loop | 32 users | 2 | 14.27 | 15.23 | 392ms | 1655ms | 13ms | 3825 | 3825 | 0 | 0 | 240 | yes |  |
| closed-loop | 32 users | 3 | 14.80 | 15.90 | 387ms | 1539ms | 12ms | 3995 | 3995 | 0 | 0 | 276 | yes |  |
