# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 2.13 | 4.81 | 926ms | 11846ms | 15ms | 1236 | 1236 | 0 | 0 | 690 | yes |  |
| closed-loop | 32 users | 2 | 2.87 | 5.27 | 719ms | 13200ms | 16ms | 1298 | 1298 | 0 | 0 | 591 | yes |  |
| closed-loop | 32 users | 3 | 2.29 | 4.90 | 893ms | 12775ms | 15ms | 1293 | 1293 | 0 | 0 | 689 | yes |  |
