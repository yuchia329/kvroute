# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 16.26 | 17.13 | 346ms | 1338ms | 12ms | 4297 | 4297 | 0 | 0 | 217 | yes |  |
| closed-loop | 32 users | 2 | 15.98 | 17.13 | 347ms | 1256ms | 12ms | 4301 | 4301 | 0 | 0 | 287 | yes |  |
| closed-loop | 32 users | 3 | 13.17 | 15.42 | 360ms | 1344ms | 12ms | 3867 | 3867 | 0 | 0 | 564 | yes |  |
