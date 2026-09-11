# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 7.50 | 9.75 | 567ms | 2137ms | 12ms | 2454 | 2454 | 0 | 0 | 568 | yes |  |
| closed-loop | 32 users | 2 | 7.57 | 9.95 | 596ms | 2004ms | 12ms | 2498 | 2498 | 0 | 0 | 597 | yes |  |
| closed-loop | 32 users | 3 | 7.75 | 9.96 | 464ms | 2001ms | 12ms | 2504 | 2504 | 0 | 0 | 556 | yes |  |
