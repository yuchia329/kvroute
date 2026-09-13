# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 12.39 | 22.29 | 110ms | 416ms | 13ms | 5597 | 5597 | 0 | 0 | 2487 | yes |  |
| closed-loop | 32 users | 2 | 34.25 | 34.31 | 87ms | 511ms | 12ms | 8606 | 8606 | 0 | 0 | 14 | yes |  |
| closed-loop | 32 users | 3 | 29.38 | 30.23 | 98ms | 633ms | 15ms | 7585 | 7585 | 0 | 0 | 211 | yes |  |
