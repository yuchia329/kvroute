# Arrival rate sweep — prefix_affinity

Driver: open-loop (offered load is an input and is held when the fleet
slows, so these numbers describe behaviour at and past saturation).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| open-loop | 6 req/s | 1 | 6.00 | 6.00 | 340ms | 746ms | 9ms | 601 | 601 | 0 | 0 | 0 | yes |  |
| open-loop | 6 req/s | 2 | 5.99 | 6.00 | 350ms | 752ms | 9ms | 601 | 601 | 0 | 0 | 1 | yes |  |
| open-loop | 6 req/s | 3 | 5.99 | 6.00 | 345ms | 662ms | 9ms | 601 | 601 | 0 | 0 | 1 | yes |  |
