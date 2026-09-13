# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 25.32 | 26.36 | 99ms | 1146ms | 13ms | 6606 | 6606 | 0 | 0 | 261 | yes |  |
| closed-loop | 32 users | 2 | 24.73 | 26.15 | 100ms | 1387ms | 13ms | 6556 | 6556 | 0 | 0 | 356 | yes |  |
| closed-loop | 32 users | 3 | 22.33 | 23.65 | 102ms | 1910ms | 13ms | 5931 | 5931 | 0 | 0 | 333 | yes |  |
