# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 25.20 | 25.41 | 95ms | 851ms | 13ms | 6377 | 6377 | 0 | 0 | 54 | yes |  |
| closed-loop | 32 users | 2 | 26.77 | 27.01 | 92ms | 971ms | 12ms | 6776 | 6776 | 0 | 0 | 62 | yes |  |
| closed-loop | 32 users | 3 | 25.48 | 25.81 | 95ms | 1031ms | 13ms | 6479 | 6479 | 0 | 0 | 81 | yes |  |
