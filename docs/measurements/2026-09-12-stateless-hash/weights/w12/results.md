# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 19.59 | 23.50 | 104ms | 724ms | 14ms | 5897 | 5897 | 0 | 0 | 982 | yes |  |
| closed-loop | 32 users | 2 | 22.68 | 26.33 | 103ms | 602ms | 13ms | 6606 | 6606 | 0 | 0 | 917 | yes |  |
| closed-loop | 32 users | 3 | 17.67 | 23.53 | 105ms | 754ms | 14ms | 5904 | 5904 | 0 | 0 | 1470 | yes |  |
