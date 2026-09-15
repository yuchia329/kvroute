# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 15.37 | 21.42 | 106ms | 715ms | 14ms | 5384 | 5384 | 0 | 0 | 1521 | yes |  |
| closed-loop | 32 users | 2 | 7.76 | 17.50 | 154ms | 543ms | 28ms | 4393 | 4393 | 0 | 0 | 2446 | yes |  |
| closed-loop | 32 users | 3 | 7.22 | 15.29 | 154ms | 813ms | 27ms | 3840 | 3840 | 0 | 0 | 2027 | yes |  |
