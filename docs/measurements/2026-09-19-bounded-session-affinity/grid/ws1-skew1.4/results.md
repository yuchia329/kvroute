# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 26.57 | 26.83 | 94ms | 989ms | 13ms | 6746 | 6746 | 0 | 0 | 65 | yes |  |
| closed-loop | 32 users | 2 | 28.35 | 28.59 | 92ms | 948ms | 13ms | 7175 | 7175 | 0 | 0 | 62 | yes |  |
| closed-loop | 32 users | 3 | 26.77 | 26.92 | 94ms | 800ms | 13ms | 6777 | 6777 | 0 | 0 | 37 | yes |  |
