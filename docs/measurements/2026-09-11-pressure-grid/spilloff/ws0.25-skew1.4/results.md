# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 16.89 | 25.33 | 96ms | 429ms | 12ms | 6361 | 6361 | 0 | 0 | 2119 | yes |  |
| closed-loop | 32 users | 2 | 16.44 | 25.07 | 98ms | 425ms | 12ms | 6291 | 6291 | 0 | 0 | 2165 | yes |  |
| closed-loop | 32 users | 3 | 19.62 | 27.70 | 95ms | 435ms | 13ms | 6953 | 6953 | 0 | 0 | 2029 | yes |  |
