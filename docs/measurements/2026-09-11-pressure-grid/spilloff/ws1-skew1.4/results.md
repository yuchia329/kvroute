# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 16.37 | 23.07 | 106ms | 530ms | 12ms | 5787 | 5787 | 0 | 0 | 1680 | yes |  |
| closed-loop | 32 users | 2 | 19.32 | 25.86 | 101ms | 513ms | 12ms | 6496 | 6496 | 0 | 0 | 1642 | yes |  |
| closed-loop | 32 users | 3 | 19.25 | 24.40 | 101ms | 559ms | 13ms | 6119 | 6119 | 0 | 0 | 1290 | yes |  |
