# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 26.90 | 26.99 | 95ms | 711ms | 14ms | 6766 | 6766 | 0 | 0 | 22 | yes |  |
| closed-loop | 32 users | 2 | 27.27 | 27.40 | 96ms | 768ms | 14ms | 6869 | 6869 | 0 | 0 | 32 | yes |  |
| closed-loop | 32 users | 3 | 26.16 | 26.26 | 97ms | 735ms | 14ms | 6585 | 6585 | 0 | 0 | 25 | yes |  |
