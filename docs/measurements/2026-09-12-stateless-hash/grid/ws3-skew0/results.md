# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 9.50 | 11.57 | 446ms | 1985ms | 13ms | 2902 | 2902 | 0 | 0 | 519 | yes |  |
| closed-loop | 32 users | 2 | 9.70 | 11.62 | 442ms | 2005ms | 13ms | 2919 | 2919 | 0 | 0 | 483 | yes |  |
| closed-loop | 32 users | 3 | 9.97 | 11.69 | 436ms | 1853ms | 13ms | 2949 | 2949 | 0 | 0 | 433 | yes |  |
