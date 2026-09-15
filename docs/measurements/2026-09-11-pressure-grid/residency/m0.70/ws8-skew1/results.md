# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.95 | 5.67 | 681ms | 14860ms | 13ms | 1464 | 1464 | 0 | 0 | 701 | yes |  |
| closed-loop | 32 users | 2 | 2.85 | 5.85 | 750ms | 12012ms | 13ms | 1524 | 1524 | 0 | 0 | 781 | yes |  |
| closed-loop | 32 users | 3 | 3.54 | 6.25 | 621ms | 11829ms | 11ms | 1572 | 1572 | 0 | 0 | 681 | yes |  |
