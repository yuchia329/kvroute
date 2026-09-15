# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 31.63 | 32.15 | 89ms | 597ms | 12ms | 8064 | 8064 | 0 | 0 | 129 | yes |  |
| closed-loop | 32 users | 2 | 22.34 | 22.83 | 106ms | 953ms | 15ms | 5741 | 5741 | 0 | 0 | 124 | yes |  |
| closed-loop | 32 users | 3 | 34.30 | 34.34 | 87ms | 478ms | 12ms | 8609 | 8609 | 0 | 0 | 12 | yes |  |
