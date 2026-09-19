# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 15.19 | 15.91 | 395ms | 1352ms | 12ms | 3994 | 3994 | 0 | 0 | 180 | yes |  |
| closed-loop | 32 users | 2 | 15.44 | 16.17 | 397ms | 1387ms | 12ms | 4065 | 4065 | 0 | 0 | 183 | yes |  |
| closed-loop | 32 users | 3 | 14.97 | 15.75 | 401ms | 1431ms | 12ms | 3954 | 3954 | 0 | 0 | 195 | yes |  |
