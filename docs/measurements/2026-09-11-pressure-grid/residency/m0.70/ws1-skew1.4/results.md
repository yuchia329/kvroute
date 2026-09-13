# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 24.88 | 25.14 | 100ms | 999ms | 13ms | 6222 | 6222 | 0 | 0 | 65 | yes |  |
| closed-loop | 32 users | 2 | 1.52 | 7.77 | 228ms | 4130ms | 34ms | 1952 | 1952 | 0 | 0 | 1569 | yes |  |
| closed-loop | 32 users | 3 | 21.55 | 22.52 | 112ms | 1593ms | 14ms | 5653 | 5653 | 0 | 0 | 243 | yes |  |
