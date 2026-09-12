# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 12.11 | 13.59 | 418ms | 1758ms | 12ms | 3416 | 3416 | 0 | 0 | 371 | yes |  |
| closed-loop | 32 users | 2 | 12.64 | 13.97 | 410ms | 1708ms | 13ms | 3505 | 3505 | 0 | 0 | 333 | yes |  |
| closed-loop | 32 users | 3 | 12.04 | 13.59 | 415ms | 1755ms | 12ms | 3406 | 3406 | 0 | 0 | 390 | yes |  |
