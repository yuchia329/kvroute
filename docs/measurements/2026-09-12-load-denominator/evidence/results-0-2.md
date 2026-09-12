# Arrival rate sweep — prefix_affinity

Driver: open-loop (offered load is an input and is held when the fleet
slows, so these numbers describe behaviour at and past saturation).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| open-loop | 6 req/s | 1 | 5.57 | 6.00 | 356ms | 1614ms | 9ms | 601 | 601 | 0 | 0 | 43 | yes |  |
| open-loop | 6 req/s | 2 | 5.30 | 6.00 | 376ms | 1767ms | 9ms | 601 | 601 | 0 | 0 | 70 | yes |  |
| open-loop | 6 req/s | 3 | 5.51 | 6.00 | 361ms | 2165ms | 9ms | 601 | 601 | 0 | 0 | 49 | yes |  |
