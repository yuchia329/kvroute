# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.78 | 4.65 | 496ms | 14061ms | 10ms | 1196 | 1196 | 0 | 0 | 481 | yes |  |
| closed-loop | 32 users | 2 | 4.11 | 6.34 | 501ms | 13583ms | 11ms | 1665 | 1665 | 0 | 0 | 586 | yes |  |
| closed-loop | 32 users | 3 | 4.52 | 6.91 | 578ms | 13334ms | 12ms | 1732 | 1732 | 0 | 0 | 599 | yes |  |
