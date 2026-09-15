# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 7.14 | 10.31 | 381ms | 1791ms | 10ms | 2594 | 2594 | 0 | 0 | 796 | yes |  |
| closed-loop | 32 users | 2 | 9.80 | 12.74 | 394ms | 1431ms | 11ms | 3198 | 3198 | 0 | 0 | 738 | yes |  |
| closed-loop | 32 users | 3 | 8.13 | 11.05 | 386ms | 1507ms | 10ms | 2785 | 2785 | 0 | 0 | 737 | yes |  |
