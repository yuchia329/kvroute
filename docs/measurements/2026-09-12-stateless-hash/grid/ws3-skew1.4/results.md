# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 26.64 | 26.96 | 96ms | 1020ms | 13ms | 6766 | 6766 | 0 | 0 | 81 | yes |  |
| closed-loop | 32 users | 2 | 27.21 | 27.51 | 92ms | 1014ms | 12ms | 6911 | 6911 | 0 | 0 | 76 | yes |  |
| closed-loop | 32 users | 3 | 26.37 | 26.72 | 95ms | 1034ms | 13ms | 6697 | 6697 | 0 | 0 | 89 | yes |  |
