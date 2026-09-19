# Concurrency sweep — bounded_session_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 16.54 | 17.28 | 352ms | 1417ms | 13ms | 4335 | 4335 | 0 | 0 | 186 | yes |  |
| closed-loop | 32 users | 2 | 16.01 | 16.84 | 360ms | 1487ms | 13ms | 4232 | 4232 | 0 | 0 | 209 | yes |  |
| closed-loop | 32 users | 3 | 15.80 | 16.72 | 355ms | 1489ms | 13ms | 4207 | 4207 | 0 | 0 | 231 | yes |  |
