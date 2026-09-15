# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.74 | 4.82 | 674ms | 16728ms | 12ms | 1229 | 1229 | 0 | 0 | 531 | yes |  |
| closed-loop | 32 users | 2 | 2.06 | 4.26 | 823ms | 14640ms | 12ms | 1122 | 1122 | 0 | 0 | 579 | yes |  |
| closed-loop | 32 users | 3 | 2.60 | 5.00 | 720ms | 12470ms | 12ms | 1255 | 1255 | 0 | 0 | 601 | yes |  |
