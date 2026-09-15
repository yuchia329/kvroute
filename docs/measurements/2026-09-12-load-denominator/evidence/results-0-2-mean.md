# Arrival rate sweep — prefix_affinity

Driver: open-loop (offered load is an input and is held when the fleet
slows, so these numbers describe behaviour at and past saturation).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| open-loop | 6 req/s | 1 | 5.66 | 6.00 | 351ms | 1560ms | 9ms | 601 | 601 | 0 | 0 | 34 | yes |  |
| open-loop | 6 req/s | 2 | 5.65 | 6.00 | 357ms | 1605ms | 9ms | 601 | 601 | 0 | 0 | 35 | yes |  |
| open-loop | 6 req/s | 3 | 5.62 | 6.00 | 353ms | 1639ms | 9ms | 601 | 601 | 0 | 0 | 38 | yes |  |
