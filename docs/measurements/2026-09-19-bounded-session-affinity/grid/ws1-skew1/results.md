# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 20.22 | 20.89 | 118ms | 1357ms | 13ms | 5240 | 5240 | 0 | 0 | 167 | yes |  |
| closed-loop | 32 users | 2 | 19.80 | 20.33 | 120ms | 1344ms | 12ms | 5110 | 5110 | 0 | 0 | 134 | yes |  |
| closed-loop | 32 users | 3 | 19.15 | 19.69 | 132ms | 1362ms | 13ms | 4939 | 4939 | 0 | 0 | 136 | yes |  |
