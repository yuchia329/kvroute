# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 6.84 | 8.77 | 430ms | 8943ms | 12ms | 2208 | 2208 | 0 | 0 | 485 | yes |  |
| closed-loop | 32 users | 2 | 6.27 | 8.25 | 423ms | 10592ms | 11ms | 2086 | 2086 | 0 | 0 | 501 | yes |  |
| closed-loop | 32 users | 3 | 7.46 | 9.23 | 428ms | 9856ms | 12ms | 2317 | 2317 | 0 | 0 | 444 | yes |  |
