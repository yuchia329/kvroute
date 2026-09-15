# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 19.93 | 22.55 | 118ms | 1086ms | 14ms | 5655 | 5655 | 0 | 0 | 656 | yes |  |
| closed-loop | 32 users | 2 | 17.24 | 18.09 | 127ms | 1217ms | 15ms | 4547 | 4547 | 0 | 0 | 213 | yes |  |
| closed-loop | 32 users | 3 | 17.69 | 20.95 | 123ms | 1052ms | 14ms | 5238 | 5238 | 0 | 0 | 814 | yes |  |
