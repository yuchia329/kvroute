# Concurrency sweep — prefix_affinity

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 32 users | 1 | 3.08 | 6.05 | 758ms | 10854ms | 15ms | 1570 | 1570 | 0 | 0 | 770 | yes |  |
| closed-loop | 32 users | 2 | 2.80 | 6.13 | 963ms | 10294ms | 14ms | 1566 | 1566 | 0 | 0 | 851 | yes |  |
| closed-loop | 32 users | 3 | 3.01 | 6.07 | 788ms | 11859ms | 15ms | 1537 | 1537 | 0 | 0 | 774 | yes |  |
