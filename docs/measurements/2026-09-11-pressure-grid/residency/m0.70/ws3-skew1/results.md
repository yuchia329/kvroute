# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.64 | 5.43 | 811ms | 12284ms | 13ms | 1401 | 1401 | 0 | 0 | 719 | yes |  |
| closed-loop | 32 users | 2 | 2.57 | 5.62 | 930ms | 11242ms | 15ms | 1459 | 1459 | 0 | 0 | 793 | yes |  |
| closed-loop | 32 users | 3 | 3.06 | 5.72 | 645ms | 13574ms | 13ms | 1437 | 1437 | 0 | 0 | 668 | yes |  |
