# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 4.46 | 6.73 | 421ms | 13026ms | 11ms | 1720 | 1720 | 0 | 0 | 581 | yes |  |
| closed-loop | 32 users | 2 | 4.44 | 6.70 | 432ms | 15089ms | 11ms | 1707 | 1707 | 0 | 0 | 575 | yes |  |
| closed-loop | 32 users | 3 | 4.29 | 6.62 | 434ms | 14193ms | 11ms | 1724 | 1724 | 0 | 0 | 607 | yes |  |
