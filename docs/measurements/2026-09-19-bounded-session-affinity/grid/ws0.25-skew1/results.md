# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 30.63 | 30.77 | 91ms | 745ms | 13ms | 7726 | 7726 | 0 | 0 | 35 | yes |  |
| closed-loop | 32 users | 2 | 30.55 | 30.61 | 90ms | 688ms | 12ms | 7683 | 7683 | 0 | 0 | 15 | yes |  |
| closed-loop | 32 users | 3 | 30.46 | 30.53 | 88ms | 679ms | 12ms | 7654 | 7654 | 0 | 0 | 17 | yes |  |
