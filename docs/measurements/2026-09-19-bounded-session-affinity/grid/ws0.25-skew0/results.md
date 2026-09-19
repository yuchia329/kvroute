# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 31.86 | 31.93 | 88ms | 557ms | 12ms | 8006 | 8006 | 0 | 0 | 18 | yes |  |
| closed-loop | 32 users | 2 | 31.43 | 31.52 | 88ms | 711ms | 12ms | 7910 | 7910 | 0 | 0 | 23 | yes |  |
| closed-loop | 32 users | 3 | 32.87 | 32.93 | 86ms | 508ms | 12ms | 8256 | 8256 | 0 | 0 | 13 | yes |  |
