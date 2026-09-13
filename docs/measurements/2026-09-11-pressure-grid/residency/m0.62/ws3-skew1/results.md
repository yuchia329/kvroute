# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 3.96 | 6.34 | 473ms | 12218ms | 15ms | 1596 | 1596 | 0 | 0 | 599 | yes | **1** |
| closed-loop | 32 users | 2 | 2.46 | 5.42 | 923ms | 11338ms | 17ms | 1358 | 1358 | 0 | 0 | 742 | yes |  |
| closed-loop | 32 users | 3 | 10.48 | 12.51 | 398ms | 6857ms | 13ms | 3140 | 3140 | 0 | 0 | 510 | yes |  |

Flagged cells — these are not averaged in without being read first:

- `prefix_affinity-c32-r1`: still warming up: the first half of the measured window was 109% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
