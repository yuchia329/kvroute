# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 27.65 | 27.78 | 91ms | 739ms | 12ms | 6966 | 6966 | 0 | 0 | 33 | yes |  |
| closed-loop | 32 users | 2 | 28.07 | 28.32 | 92ms | 958ms | 13ms | 7102 | 7102 | 0 | 0 | 64 | yes |  |
| closed-loop | 32 users | 3 | 27.42 | 27.64 | 92ms | 808ms | 13ms | 6932 | 6932 | 0 | 0 | 56 | yes |  |
