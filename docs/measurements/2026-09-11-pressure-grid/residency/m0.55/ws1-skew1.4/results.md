# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 20.01 | 24.33 | 109ms | 808ms | 13ms | 6104 | 6104 | 0 | 0 | 1083 | yes |  |
| closed-loop | 32 users | 2 | 24.60 | 25.06 | 106ms | 839ms | 15ms | 6281 | 6281 | 0 | 0 | 116 | yes |  |
| closed-loop | 32 users | 3 | 6.52 | 15.49 | 171ms | 698ms | 29ms | 3884 | 3884 | 0 | 0 | 2249 | yes |  |
