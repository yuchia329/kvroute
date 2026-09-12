# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 21.22 | 21.75 | 110ms | 1130ms | 13ms | 5458 | 5458 | 0 | 0 | 134 | yes |  |
| closed-loop | 32 users | 2 | 20.76 | 21.48 | 110ms | 1420ms | 12ms | 5392 | 5392 | 0 | 0 | 180 | yes |  |
| closed-loop | 32 users | 3 | 21.22 | 21.79 | 111ms | 1205ms | 12ms | 5464 | 5464 | 0 | 0 | 143 | yes |  |
