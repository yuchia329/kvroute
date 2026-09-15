# Arrival rate sweep — least_outstanding

Driver: open-loop (offered load is an input and is held when the fleet
slows, so these numbers describe behaviour at and past saturation).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| open-loop | 4 req/s | 1 | 4.00 | 4.00 | 332ms | 356ms | 8ms | 260 | 260 | 0 | 0 | 0 | yes |  |
| open-loop | 4 req/s | 2 | 4.00 | 4.00 | 331ms | 350ms | 8ms | 260 | 260 | 0 | 0 | 0 | yes |  |
| open-loop | 4 req/s | 3 | 4.00 | 4.00 | 330ms | 352ms | 8ms | 260 | 260 | 0 | 0 | 0 | yes |  |
| open-loop | 8 req/s | 1 | 8.00 | 8.00 | 359ms | 679ms | 9ms | 520 | 520 | 0 | 0 | 0 | yes |  |
| open-loop | 8 req/s | 2 | 8.00 | 8.00 | 361ms | 617ms | 9ms | 520 | 520 | 0 | 0 | 0 | yes |  |
| open-loop | 8 req/s | 3 | 8.00 | 8.00 | 360ms | 637ms | 9ms | 520 | 520 | 0 | 0 | 0 | yes |  |
| open-loop | 12 req/s | 1 | 10.72 | 12.00 | 525ms | 1353ms | 9ms | 781 | 781 | 0 | 0 | 83 | yes |  |
| open-loop | 12 req/s | 2 | 11.19 | 12.00 | 532ms | 1230ms | 9ms | 781 | 781 | 0 | 0 | 53 | yes |  |
| open-loop | 12 req/s | 3 | 11.39 | 12.00 | 500ms | 1172ms | 9ms | 781 | 781 | 0 | 0 | 40 | yes |  |
| open-loop | 16 req/s | 1 | 0.02 | 16.00 | 2589ms | 6916ms | 580ms | 1040 | 1040 | 0 | 0 | 1039 | yes |  |
| open-loop | 16 req/s | 2 | 0.37 | 16.00 | 2201ms | 8000ms | 419ms | 1040 | 1040 | 0 | 0 | 1016 | yes |  |
| open-loop | 16 req/s | 3 | 0.05 | 16.00 | 2249ms | 9104ms | 384ms | 1040 | 1040 | 0 | 0 | 1037 | yes |  |
| open-loop | 24 req/s | 1 | 0.00 | 24.00 | 33585ms | 63205ms | 621ms | 1561 | 1561 | 0 | 0 | 1561 | yes |  |
| open-loop | 24 req/s | 2 | 0.00 | 24.00 | 32822ms | 61012ms | 618ms | 1561 | 1561 | 0 | 0 | 1561 | yes |  |
| open-loop | 24 req/s | 3 | 0.00 | 24.00 | 32656ms | 60057ms | 620ms | 1561 | 1561 | 0 | 0 | 1561 | yes |  |
| open-loop | 32 req/s | 1 | 0.00 | 32.00 | 65073ms | 116327ms | 620ms | 2080 | 2080 | 0 | 0 | 2080 | yes |  |
| open-loop | 32 req/s | 2 | 0.00 | 32.00 | 64175ms | 116522ms | 622ms | 2080 | 2080 | 0 | 0 | 2080 | yes |  |
| open-loop | 32 req/s | 3 | 0.00 | 32.00 | 63872ms | 118281ms | 622ms | 2080 | 2080 | 0 | 0 | 2080 | yes |  |
| open-loop | 48 req/s | 1 | 0.00 | 48.00 | 127289ms | 230785ms | 618ms | 3121 | 3121 | 0 | 0 | 3121 | yes |  |
| open-loop | 48 req/s | 2 | 0.00 | 48.00 | 126409ms | 227636ms | 624ms | 3121 | 3121 | 0 | 0 | 3121 | yes |  |
| open-loop | 48 req/s | 3 | 0.00 | 48.00 | 126356ms | 228449ms | 623ms | 3121 | 3121 | 0 | 0 | 3121 | yes |  |
