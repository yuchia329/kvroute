# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.94 | 5.34 | 676ms | 16154ms | 12ms | 1368 | 1368 | 0 | 0 | 615 | yes |  |
| closed-loop | 32 users | 2 | 2.75 | 5.24 | 669ms | 13363ms | 12ms | 1342 | 1342 | 0 | 0 | 638 | yes |  |
| closed-loop | 32 users | 3 | 2.74 | 5.08 | 672ms | 14792ms | 12ms | 1307 | 1307 | 0 | 0 | 601 | yes |  |
