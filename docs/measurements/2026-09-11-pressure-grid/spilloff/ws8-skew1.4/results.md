# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 23.66 | 26.06 | 96ms | 761ms | 12ms | 6539 | 6539 | 0 | 0 | 603 | yes |  |
| closed-loop | 32 users | 2 | 21.31 | 24.16 | 97ms | 756ms | 13ms | 6059 | 6059 | 0 | 0 | 714 | yes |  |
| closed-loop | 32 users | 3 | 24.58 | 26.70 | 97ms | 752ms | 13ms | 6698 | 6698 | 0 | 0 | 531 | yes |  |
