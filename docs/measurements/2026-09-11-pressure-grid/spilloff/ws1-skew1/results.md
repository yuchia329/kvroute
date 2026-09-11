# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 21.02 | 22.21 | 98ms | 1104ms | 12ms | 5568 | 5568 | 0 | 0 | 298 | yes |  |
| closed-loop | 32 users | 2 | 10.55 | 16.12 | 142ms | 1074ms | 11ms | 4049 | 4049 | 0 | 0 | 1399 | yes |  |
| closed-loop | 32 users | 3 | 20.80 | 21.90 | 97ms | 1088ms | 12ms | 5494 | 5494 | 0 | 0 | 278 | yes |  |
