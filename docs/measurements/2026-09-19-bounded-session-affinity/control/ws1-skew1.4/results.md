# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 30.86 | 30.87 | 90ms | 621ms | 12ms | 7745 | 7745 | 0 | 0 | 3 | yes |  |
| closed-loop | 32 users | 2 | 32.01 | 32.05 | 87ms | 641ms | 12ms | 8033 | 8033 | 0 | 0 | 8 | yes |  |
| closed-loop | 32 users | 3 | 30.57 | 30.63 | 89ms | 705ms | 12ms | 7687 | 7687 | 0 | 0 | 14 | yes |  |
