# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 21.90 | 22.30 | 109ms | 1101ms | 13ms | 5593 | 5593 | 0 | 0 | 102 | yes |  |
| closed-loop | 32 users | 2 | 27.43 | 27.55 | 97ms | 765ms | 12ms | 6898 | 6898 | 0 | 0 | 30 | yes |  |
| closed-loop | 32 users | 3 | 26.56 | 26.73 | 99ms | 912ms | 13ms | 6702 | 6702 | 0 | 0 | 44 | yes |  |
