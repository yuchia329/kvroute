# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 25.37 | 25.65 | 96ms | 1007ms | 13ms | 6429 | 6429 | 0 | 0 | 71 | yes |  |
| closed-loop | 32 users | 2 | 24.92 | 25.19 | 97ms | 1017ms | 12ms | 6319 | 6319 | 0 | 0 | 70 | yes |  |
| closed-loop | 32 users | 3 | 24.92 | 25.23 | 96ms | 1024ms | 13ms | 6326 | 6326 | 0 | 0 | 77 | yes |  |
