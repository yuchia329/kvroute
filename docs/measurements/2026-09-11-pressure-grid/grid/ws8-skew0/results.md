# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 7.09 | 9.78 | 624ms | 2208ms | 12ms | 2459 | 2459 | 0 | 0 | 676 | yes |  |
| closed-loop | 32 users | 2 | 7.02 | 9.82 | 672ms | 2144ms | 12ms | 2469 | 2469 | 0 | 0 | 704 | yes |  |
| closed-loop | 32 users | 3 | 7.44 | 9.71 | 462ms | 2133ms | 12ms | 2444 | 2444 | 0 | 0 | 573 | yes |  |
