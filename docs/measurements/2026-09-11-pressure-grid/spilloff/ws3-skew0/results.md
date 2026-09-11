# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 5.97 | 8.51 | 403ms | 2183ms | 9ms | 2142 | 2142 | 0 | 0 | 639 | yes |  |
| closed-loop | 32 users | 2 | 6.21 | 8.79 | 407ms | 2227ms | 10ms | 2224 | 2224 | 0 | 0 | 653 | yes |  |
| closed-loop | 32 users | 3 | 5.75 | 8.44 | 405ms | 2192ms | 10ms | 2127 | 2127 | 0 | 0 | 677 | yes |  |
