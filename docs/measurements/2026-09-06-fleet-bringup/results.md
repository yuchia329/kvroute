# Concurrency sweep — round_robin

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: none applied — goodput is not reported and SLO violations were not counted

| conc | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| 1 | 1 | — | 1.85 | 320ms | 330ms | 8ms | 30 | 30 | 0 | 0 | — | yes |  |
| 8 | 1 | — | 11.58 | 328ms | 364ms | 8ms | 189 | 189 | 0 | 0 | — | yes |  |
