# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 27.58 | 29.97 | 101ms | 445ms | 14ms | 7514 | 7514 | 0 | 0 | 599 | yes |  |
| closed-loop | 32 users | 2 | 29.03 | 29.71 | 92ms | 638ms | 13ms | 7460 | 7460 | 0 | 0 | 171 | yes |  |
| closed-loop | 32 users | 3 | 27.86 | 28.62 | 101ms | 516ms | 14ms | 7177 | 7177 | 0 | 0 | 192 | yes |  |
