# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 9.43 | 11.20 | 439ms | 2087ms | 13ms | 2834 | 2834 | 0 | 0 | 446 | yes |  |
| closed-loop | 32 users | 2 | 9.56 | 11.32 | 436ms | 1791ms | 13ms | 2849 | 2849 | 0 | 0 | 444 | yes |  |
| closed-loop | 32 users | 3 | 9.44 | 11.23 | 438ms | 1859ms | 13ms | 2823 | 2823 | 0 | 0 | 449 | yes |  |
