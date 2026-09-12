# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 21.07 | 21.72 | 102ms | 1365ms | 12ms | 5476 | 5476 | 0 | 0 | 163 | yes |  |
| closed-loop | 32 users | 2 | 21.48 | 22.25 | 97ms | 1403ms | 12ms | 5582 | 5582 | 0 | 0 | 193 | yes |  |
| closed-loop | 32 users | 3 | 21.26 | 22.06 | 99ms | 1404ms | 12ms | 5537 | 5537 | 0 | 0 | 202 | yes |  |
