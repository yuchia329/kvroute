# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 16.93 | 17.82 | 349ms | 1488ms | 12ms | 4479 | 4479 | 0 | 0 | 225 | yes |  |
| closed-loop | 32 users | 2 | 16.87 | 17.91 | 343ms | 1542ms | 12ms | 4507 | 4507 | 0 | 0 | 263 | yes |  |
| closed-loop | 32 users | 3 | 16.93 | 17.82 | 341ms | 1494ms | 12ms | 4473 | 4473 | 0 | 0 | 223 | yes |  |
