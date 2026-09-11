# Concurrency sweep — round_robin

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 10.86 | 14.31 | 216ms | 2611ms | 12ms | 3597 | 3597 | 0 | 0 | 866 | yes |  |
| closed-loop | 32 users | 2 | 11.53 | 14.82 | 224ms | 2450ms | 12ms | 3728 | 3728 | 0 | 0 | 828 | yes |  |
| closed-loop | 32 users | 3 | 11.23 | 14.55 | 277ms | 2678ms | 12ms | 3656 | 3656 | 0 | 0 | 833 | yes |  |
