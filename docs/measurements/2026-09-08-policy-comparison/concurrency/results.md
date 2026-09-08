# Concurrency sweep — least_outstanding

Driver: closed-loop (offered load is an outcome, so the tail here is
optimistic; the headline goodput number comes from the open-loop driver).

SLO: TTFT < 990ms, inter-token p50 < 24ms

| driver | load | rep | goodput/s | tput/s | TTFT p50 | TTFT p99 | ITL p50 | reqs | ok | dropped | failed | SLO viol | clean | flagged |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|:--:|:--|
| closed-loop | 1 users | 1 | 1.26 | 1.26 | 324ms | 335ms | 8ms | 82 | 82 | 0 | 0 | 0 | yes |  |
| closed-loop | 1 users | 2 | 1.28 | 1.28 | 325ms | 334ms | 8ms | 83 | 83 | 0 | 0 | 0 | yes |  |
| closed-loop | 1 users | 3 | 1.34 | 1.34 | 325ms | 337ms | 8ms | 87 | 87 | 0 | 0 | 0 | yes |  |
| closed-loop | 4 users | 1 | 5.08 | 5.08 | 330ms | 369ms | 8ms | 332 | 332 | 0 | 0 | 0 | yes |  |
| closed-loop | 4 users | 2 | 4.93 | 4.93 | 333ms | 387ms | 8ms | 325 | 325 | 0 | 0 | 0 | yes |  |
| closed-loop | 4 users | 3 | 4.99 | 4.99 | 332ms | 396ms | 8ms | 326 | 326 | 0 | 0 | 0 | yes |  |
| closed-loop | 8 users | 1 | 8.01 | 8.01 | 360ms | 664ms | 8ms | 528 | 528 | 0 | 0 | 0 | yes |  |
| closed-loop | 8 users | 2 | 8.03 | 8.03 | 360ms | 723ms | 8ms | 527 | 527 | 0 | 0 | 0 | yes |  |
| closed-loop | 8 users | 3 | 7.92 | 7.92 | 363ms | 694ms | 8ms | 521 | 521 | 0 | 0 | 0 | yes |  |
| closed-loop | 16 users | 1 | 10.67 | 10.79 | 386ms | 991ms | 9ms | 711 | 711 | 0 | 0 | 8 | yes |  |
| closed-loop | 16 users | 2 | 10.56 | 10.73 | 377ms | 1041ms | 9ms | 712 | 712 | 0 | 0 | 11 | yes |  |
| closed-loop | 16 users | 3 | 10.52 | 10.72 | 386ms | 1068ms | 9ms | 708 | 708 | 0 | 0 | 13 | yes |  |
| closed-loop | 32 users | 1 | 10.57 | 12.96 | 542ms | 1632ms | 10ms | 860 | 860 | 0 | 0 | 159 | yes |  |
| closed-loop | 32 users | 2 | 9.84 | 13.04 | 553ms | 2052ms | 10ms | 859 | 859 | 0 | 0 | 211 | yes |  |
| closed-loop | 32 users | 3 | 10.28 | 13.02 | 550ms | 1602ms | 10ms | 866 | 866 | 0 | 0 | 182 | yes |  |
| closed-loop | 64 users | 1 | 9.31 | 14.31 | 720ms | 2567ms | 12ms | 954 | 954 | 0 | 0 | 333 | yes |  |
| closed-loop | 64 users | 2 | 8.21 | 14.35 | 874ms | 2572ms | 12ms | 951 | 951 | 0 | 0 | 407 | yes |  |
| closed-loop | 64 users | 3 | 8.17 | 14.32 | 881ms | 2535ms | 12ms | 948 | 948 | 0 | 0 | 407 | yes |  |
| closed-loop | 128 users | 1 | 6.41 | 14.72 | 1077ms | 2788ms | 19ms | 980 | 980 | 0 | 0 | 553 | yes |  |
| closed-loop | 128 users | 2 | 5.53 | 14.74 | 1341ms | 2644ms | 19ms | 983 | 983 | 0 | 0 | 614 | yes |  |
| closed-loop | 128 users | 3 | 6.64 | 14.54 | 1051ms | 2801ms | 19ms | 977 | 977 | 0 | 0 | 531 | yes |  |
| closed-loop | 256 users | 1 | 0.37 | 14.25 | 1543ms | 3195ms | 188ms | 961 | 961 | 0 | 0 | 936 | yes |  |
| closed-loop | 256 users | 2 | 0.50 | 14.19 | 1570ms | 3249ms | 61ms | 959 | 959 | 0 | 0 | 925 | yes |  |
| closed-loop | 256 users | 3 | 0.35 | 14.12 | 1618ms | 3148ms | 148ms | 971 | 971 | 0 | 0 | 947 | yes |  |
