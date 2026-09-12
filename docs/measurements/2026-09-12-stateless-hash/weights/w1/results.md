# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 26.80 | 27.19 | 91ms | 1037ms | 12ms | 6824 | 6824 | 0 | 0 | 97 | yes |  |
| closed-loop | 32 users | 2 | 27.85 | 28.27 | 89ms | 1074ms | 12ms | 7103 | 7103 | 0 | 0 | 106 | yes |  |
| closed-loop | 32 users | 3 | 26.79 | 27.24 | 91ms | 1066ms | 12ms | 6837 | 6837 | 0 | 0 | 113 | yes |  |
