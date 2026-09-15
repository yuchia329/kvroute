# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 17.97 | 25.74 | 105ms | 438ms | 13ms | 6456 | 6456 | 0 | 0 | 1948 | yes |  |
| closed-loop | 32 users | 2 | 10.02 | 19.42 | 141ms | 436ms | 18ms | 4874 | 4874 | 0 | 0 | 2360 | yes |  |
| closed-loop | 32 users | 3 | 12.37 | 22.73 | 120ms | 466ms | 13ms | 5704 | 5704 | 0 | 0 | 2599 | yes |  |
