# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.81 | 5.25 | 706ms | 12815ms | 12ms | 1335 | 1335 | 0 | 0 | 621 | yes |  |
| closed-loop | 32 users | 2 | 2.62 | 4.91 | 674ms | 13305ms | 11ms | 1232 | 1232 | 0 | 0 | 575 | yes |  |
| closed-loop | 32 users | 3 | 2.92 | 5.24 | 706ms | 19844ms | 13ms | 1332 | 1332 | 0 | 0 | 589 | yes |  |
