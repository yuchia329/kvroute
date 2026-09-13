# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 6.15 | 7.91 | 411ms | 15035ms | 11ms | 1990 | 1990 | 0 | 0 | 443 | yes |  |
| closed-loop | 32 users | 2 | 5.12 | 7.21 | 426ms | 14203ms | 11ms | 1819 | 1819 | 0 | 0 | 526 | yes |  |
| closed-loop | 32 users | 3 | 5.94 | 7.84 | 428ms | 10202ms | 11ms | 2010 | 2010 | 0 | 0 | 487 | yes |  |
