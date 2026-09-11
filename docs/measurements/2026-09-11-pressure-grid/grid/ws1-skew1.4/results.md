# Concurrency sweep — session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 13.48 | 20.01 | 111ms | 553ms | 15ms | 5023 | 5023 | 0 | 0 | 1639 | yes |  |
| closed-loop | 32 users | 2 | 10.89 | 19.23 | 130ms | 647ms | 16ms | 4826 | 4826 | 0 | 0 | 2092 | yes |  |
| closed-loop | 32 users | 3 | 11.23 | 18.86 | 117ms | 645ms | 13ms | 4739 | 4739 | 0 | 0 | 1918 | yes |  |
