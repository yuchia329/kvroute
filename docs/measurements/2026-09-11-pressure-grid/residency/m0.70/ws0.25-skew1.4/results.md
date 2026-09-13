# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 32.43 | 32.44 | 93ms | 470ms | 13ms | 8142 | 8142 | 0 | 0 | 1 | yes |  |
| closed-loop | 32 users | 2 | 32.16 | 32.45 | 90ms | 443ms | 12ms | 8139 | 8139 | 0 | 0 | 74 | yes |  |
| closed-loop | 32 users | 3 | 32.48 | 32.55 | 93ms | 656ms | 13ms | 8156 | 8156 | 0 | 0 | 16 | yes |  |
