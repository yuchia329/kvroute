# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 5.83 | 8.57 | 399ms | 1644ms | 9ms | 871 | 871 | 0 | 0 | 278 | yes |  |
| closed-loop | 32 users | 2 | 5.31 | 7.96 | 402ms | 2129ms | 9ms | 817 | 817 | 0 | 0 | 272 | yes |  |
| closed-loop | 32 users | 3 | 6.05 | 8.65 | 401ms | 1802ms | 10ms | 878 | 878 | 0 | 0 | 264 | yes |  |
