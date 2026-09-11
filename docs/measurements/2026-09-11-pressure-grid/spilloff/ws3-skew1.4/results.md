# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 21.81 | 25.01 | 101ms | 794ms | 12ms | 6272 | 6272 | 0 | 0 | 802 | yes |  |
| closed-loop | 32 users | 2 | 22.95 | 25.94 | 98ms | 691ms | 13ms | 6498 | 6498 | 0 | 0 | 749 | yes |  |
| closed-loop | 32 users | 3 | 10.78 | 20.06 | 139ms | 690ms | 13ms | 5036 | 5036 | 0 | 0 | 2331 | yes |  |
