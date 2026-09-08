# GPU 3 throttles thermally under simultaneous load — 2026-09-07

Replica-3 is measurably slower than the other five, but **only when all six cards are drawing power
at once**, and the cause is thermal throttling on GPU 3 rather than anything about NUMA, CPU
allocation or the router.

Two arms on one fleet, minutes apart, with nvidia-smi sampled every 2 s throughout. Every figure
below is recomputable with `python3 analyse.py` from the rows in this directory.

| | [`solo/`](solo/) | [`together/`](together/) |
|---|---|---|
| schedule | one replica at a time | all six at once |
| level | concurrency 1 | concurrency 1 |
| probes | 60 s, 15 s warm-up, 1 repetition | 60 s, 15 s warm-up, 3 repetitions |
| workload seed | 1 | 2 — different bytes, so this arm cannot read the other's prefix cache |

Both arms ran unpinned, on the fleet's normal configuration, on a freshly started fleet. Prefix
cache hit rate was 1.3% in every probe, against the 5% limit, and every probe was clean.

## The measurement

**Driven one at a time, the fleet is symmetric.** Replica-3 is nominally the slowest, by an amount
that means nothing:

| replica | TTFT p50 |
|---|---:|
| replica-0 | 324.6 ms |
| replica-1 | 325.9 ms |
| replica-2 | 323.6 ms |
| **replica-3** | **327.7 ms** |
| replica-4 | 325.1 ms |
| replica-5 | 322.8 ms |

Spread **1.5%**, against an 8% tolerance. That agrees with the two earlier independent runs: 2.1% in
the [fleet bring-up](../2026-09-06-fleet-bringup/) and 2.3% in the
[solo characterization](../2026-09-07-characterization/).

**Driven all six at once, replica-3 degrades as the run proceeds:**

| repetition | replica-3 | fleet min | gap |
|---|---:|---:|---:|
| r1 | 330.2 ms | 326.7 ms | **1.1%** |
| r2 | 381.3 ms | 338.4 ms | **12.7%** |
| r3 | 400.4 ms | 341.7 ms | **17.2%** |

It starts healthy and gets worse. Its inter-token latency rises with it, 7.81 ms to 8.71 ms. The
whole fleet drifts upward — replica-0 goes 329 → 354 → 364 ms — but GPU 3 goes furthest and fastest.

## The cause

nvidia-smi during the together arm:

| | GPU 0 | GPU 1 | GPU 2 | **GPU 3** | GPU 4 | GPU 5 |
|---|---:|---:|---:|---:|---:|---:|
| SM clock, mean | 1498 | 1532 | 1534 | **1403** | 1522 | 1530 |
| SM clock, min | 1305 | 1350 | 1380 | **960** | 1335 | 1350 |
| core temp, max | 77 °C | 76 °C | 80 °C | **75 °C** | 83 °C | 79 °C |
| power, mean | 274 W | 276 W | 273 W | **260 W** | 278 W | 278 W |

And the throttle reasons the driver itself reports:

| GPU | throttle reasons, share of samples |
|---|---|
| 0 | SwPowerCap 74%, SwThermal 29% |
| 1 | SwPowerCap 98%, SwThermal 3% |
| 2 | SwPowerCap 67%, SwThermal 34% |
| **3** | **SwThermal 67%**, SwPowerCap 35% |
| 4 | SwPowerCap 100% |
| 5 | SwPowerCap 99%, SwThermal 1% |

All six sit at the same 280 W limit, so power capping is normal, expected and equal across the
fleet. **GPU 3 is the only card predominantly limited by heat**, and it hits that limit at a *lower*
core temperature (75 °C) than GPU 4 tolerates without throttling at all (83 °C). A card that
throttles thermally while running cooler than a card that does not is not being limited by the
temperature being reported — the likely candidates are GDDR6X memory-junction temperature, which
nvidia-smi does not expose on this driver, or a cooling defect in that card or its slot (pads,
dust, airflow).

The first third of the telemetry, which is mostly the solo arm, shows the same card idling at
54 °C and holding 1646 MHz — its highest clock of the run. Nothing is wrong with GPU 3 when its
neighbours are quiet.

## Why this matters more than its size suggests

The aggregate cost is small. In the [contention runs](../2026-09-07-numa-contention/) replica-3
sustained 1.63 and 1.60 rps at concurrency 32 against a healthy-replica mean of 1.94 — a **16–17%
deficit**, which is one card at 83% out of six, or a fleet at 97.2% of nominal. Paid equally by
every policy, that would be a rounding error.

It is not paid equally, for three reasons.

**Policies differ in how much traffic they send it.** Round-robin sends exactly 1/6 by
construction and consistent-hash about 1/6 and *stickily*, so the sessions that land there stay
there. Least-outstanding and prefix-affinity-with-spill both watch inflight, and a slow replica
accumulates inflight, so both automatically route away from it. The two load-aware policies get an
advantage that scales with how bad the card is.

**It manufactures the primary mechanism inside the control condition.** `idea.md` §5 names load
imbalance under skew as the mechanism the project exists to measure, and P4's claimed advantage
over P3 is precisely that it has an escape hatch when P3 does not. A throttling card is a permanent
load imbalance with no escape hatch for P3 — present in every cell, including the skew-zero cells
that are supposed to establish the baseline.

**It is not constant.** The deficit grew 1.1% → 12.7% → 17.2% across three repetitions in about
three minutes. Each policy is a separate sweep run, hours apart, so thermal state correlates with
which policy is being measured rather than cancelling across them.

## What this rules out

- **NUMA.** The effect is absent when replicas are driven one at a time, which is when node-local
  CPU is most abundant, and present when they are driven together. It also does not follow the node
  boundary: GPU 3 is on node 0 alongside GPUs 0–2, which are fine.
- **CPU allocation.** Pinning every replica to twelve disjoint threads of its own node left
  replica-3 the slowest in 39 of 41 five-second slices, unchanged from unpinned's 38 of 41.
- **The card being intrinsically slow.** It is the fastest-clocking card in the fleet when its
  neighbours are idle, and it was the *fastest* replica at concurrency 1 in the
  [bring-up run](../2026-09-06-fleet-bringup/).

## The fix we cannot apply

Capping every card at a wattage GPU 3 sustains would make the six equal by construction — the same
idea as CPU pinning, applied to the resource that is actually scarce. The limit is settable in
principle:

```
power.limit 280.00 W, power.min_limit 100.00 W, power.max_limit 350.00 W
```

but not by us:

```
$ nvidia-smi -i 3 -pl 250
Failed to set power management limit for GPU 00000000:40:00.0: Insufficient Permissions
$ sudo -n true
sudo: a password is required
```

So the options are to ask whoever administers the box for a uniform power cap or physical attention
to GPU 3; to run a symmetric subset without it (0,1,2,4,5 or 0,1,4,5); or to keep all six and make
the throttling recorded evidence rather than an invisible confound. Tracked in the follow-up issue.

## Reproducing

```sh
ops/fleet.sh up
nvidia-smi --query-gpu=index,timestamp,temperature.gpu,clocks.sm,power.draw,power.limit,utilization.gpu,clocks_throttle_reasons.active \
  --format=csv,noheader -l 2 > run/gpusample.csv &

bin/characterize-linux-amd64 -dir runs/rep3-solo -schedule solo -levels 1 \
  -repetitions 1 -probe-duration 60s -warmup 15s -seed 1 \
  -model "$(ops/fleet.sh env MODEL)" -gpus "$(ops/fleet.sh env REPLICA_COUNT)" \
  -replicas "$(ops/fleet.sh replicas)"

bin/characterize-linux-amd64 -dir runs/rep3-together -schedule together -levels 1 \
  -repetitions 3 -probe-duration 60s -warmup 15s -seed 2 \
  -model "$(ops/fleet.sh env MODEL)" -gpus "$(ops/fleet.sh env REPLICA_COUNT)" \
  -replicas "$(ops/fleet.sh replicas)"
```

The seeds differ on purpose: identical seeds send identical bytes, and the second arm would then be
reading the first arm's prefix cache rather than measuring prefill (ADR-0004).
