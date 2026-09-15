# Concurrency sweep — prefix_hash

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 32.69 | 32.77 | 86ms | 472ms | 12ms | 8219 | 8219 | 0 | 0 | 20 | yes |  |
| closed-loop | 32 users | 2 | 32.65 | 32.79 | 87ms | 567ms | 13ms | 8227 | 8227 | 0 | 0 | 37 | yes |  |
| closed-loop | 32 users | 3 | 33.41 | 33.52 | 89ms | 470ms | 13ms | 8410 | 8410 | 0 | 0 | 28 | yes |  |
