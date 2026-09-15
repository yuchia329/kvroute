# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 9.07 | 11.09 | 443ms | 2077ms | 12ms | 2791 | 2791 | 0 | 0 | 508 | yes |  |
| closed-loop | 32 users | 2 | 9.12 | 11.09 | 442ms | 1862ms | 12ms | 2784 | 2784 | 0 | 0 | 494 | yes |  |
| closed-loop | 32 users | 3 | 9.31 | 11.03 | 440ms | 1807ms | 13ms | 2770 | 2770 | 0 | 0 | 432 | yes |  |
