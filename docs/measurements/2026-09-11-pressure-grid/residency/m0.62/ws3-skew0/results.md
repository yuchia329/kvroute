# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.97 | 4.99 | 511ms | 16847ms | 11ms | 1284 | 1284 | 0 | 0 | 520 | yes |  |
| closed-loop | 32 users | 2 | 3.77 | 5.94 | 475ms | 15996ms | 11ms | 1576 | 1576 | 0 | 0 | 576 | yes |  |
| closed-loop | 32 users | 3 | 3.37 | 5.61 | 539ms | 16385ms | 11ms | 1465 | 1465 | 0 | 0 | 586 | yes |  |
