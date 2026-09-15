# Arrival rate sweep — prefix_affinity

Driver: open-loop (offered load is an input and is held when the fleet
slows, so these numbers describe behaviour at and past saturation).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| open-loop | 6 req/s | 1 | 5.97 | 6.00 | 331ms | 756ms | 9ms | 601 | 601 | 0 | 0 | 3 | yes |  |
| open-loop | 6 req/s | 2 | 5.89 | 6.00 | 347ms | 1312ms | 9ms | 601 | 601 | 0 | 0 | 11 | yes |  |
| open-loop | 6 req/s | 3 | 5.89 | 6.00 | 348ms | 1347ms | 9ms | 601 | 601 | 0 | 0 | 11 | yes |  |
