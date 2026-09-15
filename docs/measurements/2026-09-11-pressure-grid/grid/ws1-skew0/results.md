# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 7.84 | 10.21 | 469ms | 2007ms | 12ms | 2571 | 2571 | 0 | 0 | 596 | yes |  |
| closed-loop | 32 users | 2 | 8.16 | 10.43 | 445ms | 2130ms | 12ms | 2622 | 2622 | 0 | 0 | 570 | yes |  |
| closed-loop | 32 users | 3 | 7.71 | 10.25 | 586ms | 2135ms | 12ms | 2597 | 2597 | 0 | 0 | 645 | yes |  |
