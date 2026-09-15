# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 32.80 | 32.93 | 88ms | 682ms | 12ms | 8254 | 8254 | 0 | 0 | 32 | yes |  |
| closed-loop | 32 users | 2 | 11.28 | 17.19 | 135ms | 1062ms | 19ms | 4311 | 4311 | 0 | 0 | 1482 | yes |  |
| closed-loop | 32 users | 3 | 19.48 | 26.35 | 90ms | 430ms | 11ms | 6621 | 6621 | 0 | 0 | 1727 | yes |  |
