# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 35.16 | 35.33 | 83ms | 145ms | 12ms | 8862 | 8862 | 0 | 0 | 43 | yes |  |
| closed-loop | 32 users | 2 | 35.32 | 35.32 | 85ms | 142ms | 13ms | 8857 | 8857 | 0 | 0 | 2 | yes |  |
| closed-loop | 32 users | 3 | 35.04 | 35.21 | 84ms | 143ms | 13ms | 8831 | 8831 | 0 | 0 | 42 | yes |  |
