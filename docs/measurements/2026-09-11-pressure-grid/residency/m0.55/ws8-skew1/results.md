# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 3.98 | 7.15 | 470ms | 2419ms | 20ms | 1798 | 1798 | 0 | 0 | 797 | yes |  |
| closed-loop | 32 users | 2 | 12.61 | 14.30 | 406ms | 1860ms | 13ms | 3560 | 3560 | 0 | 0 | 421 | yes |  |
| closed-loop | 32 users | 3 | 4.86 | 6.97 | 458ms | 6886ms | 16ms | 1750 | 1750 | 0 | 0 | 531 | yes | **1** |

Flagged cells — these are not averaged in without being read first:

- `prefix_affinity-c32-r3`: still warming up: the first half of the measured window was 111% slower than the second by TTFT p50, over a 25% threshold. Lengthen the warm-up and re-run
