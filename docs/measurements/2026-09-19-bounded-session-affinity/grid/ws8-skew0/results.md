# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 9.04 | 10.93 | 445ms | 2127ms | 13ms | 2746 | 2746 | 0 | 0 | 474 | yes |  |
| closed-loop | 32 users | 2 | 8.80 | 10.82 | 447ms | 2022ms | 13ms | 2717 | 2717 | 0 | 0 | 509 | yes |  |
| closed-loop | 32 users | 3 | 8.58 | 10.63 | 453ms | 2174ms | 13ms | 2671 | 2671 | 0 | 0 | 516 | yes |  |
