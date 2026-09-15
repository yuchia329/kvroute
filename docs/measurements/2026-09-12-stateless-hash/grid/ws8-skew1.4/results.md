# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 25.65 | 25.97 | 97ms | 1009ms | 13ms | 6514 | 6514 | 0 | 0 | 79 | yes |  |
| closed-loop | 32 users | 2 | 25.42 | 25.74 | 97ms | 1023ms | 12ms | 6451 | 6451 | 0 | 0 | 79 | yes |  |
| closed-loop | 32 users | 3 | 25.22 | 25.61 | 99ms | 1067ms | 13ms | 6424 | 6424 | 0 | 0 | 98 | yes |  |
