# Box-side sweep drivers

These run on the GPU box (`ssh nlp`), from `~/kvroute`, not from a checkout. The box has no
Go toolchain and `~/kvroute` there is not a git clone: it holds scp'd copies of the
`Makefile`, `ops/` and the cross-compiled `bin/*-linux-amd64`, plus these scripts at its
root. To use one, copy it to `~/kvroute/` on the box and run it from there — every script
sources `./lib-sweep.sh` relative to that directory, and calls `./ops/fleet.sh` and
`./bin/…` the same way.

## Getting the repo onto the box

From a checkout, on a machine with Go:

    make box-sync            # cross-compiles every command, then rsyncs Makefile, ops/ and bin/

It copies those three and nothing else — no `--delete`, and nothing at the box's top level,
where the run directories, the logs and these drivers' live copies are. Point it elsewhere
with `BOX_HOST=` and `BOX_DIR=`.

Once it has run, the Makefile's run targets work on the box the way they do on a
workstation. They detect the missing toolchain and run `bin/<command>-linux-amd64` instead
of building (`BOX=1` forces it, `BOX=0` forces the other way), so `make bench`,
`make pressure-grid`, `make chaos`, `ops/pressure-grid.sh` and `ops/chaos.sh` are all
usable there. That is what these scripts existed to work around; what is left in them is
the fleet and router lifecycle around a sweep, which the targets deliberately do not touch.

## Checking box mode without a fleet

`make bench`, `make pressure-grid` and `make chaos` were confirmed on the box on 2026-09-12,
against a fake replica rather than the cards — half of them were another user's that day,
and none of the three needs a GPU to prove that it runs. `test/box` pins the same thing from
`make -n`, but only the box can show that `build` really steps aside and that the
cross-compiled binary really executes. To repeat it, from `~/kvroute` on the box:

    make run-fake &                                              # bin/fakereplica-linux-amd64 on :8000
    make run-router POLICY=prefix_affinity LOAD_IMBALANCE=2 \
        REPLICAS=replica-0=http://127.0.0.1:8000 RUN_DIR=runs/box-check &
    sleep 3                                                      # both are up before anything drives them
    SMOKE='-replicas replica-0=http://127.0.0.1:8000 -concurrency 1 -cell-duration 30s -warmup 5s -sample-gpus=false'
    make bench POLICY=prefix_affinity RUN_DIR=runs/box-check REPS=1 \
        LOAD_IMBALANCE=2 BENCH_ARGS="$SMOKE"
    make pressure-grid POLICY=prefix_affinity PRESSURE_DIR=runs/box-check/pressure \
        PRESSURE_WS=1 PRESSURE_SKEW=0 KV_CAPACITY=629760 REPS=1 \
        BENCH_ARGS="$SMOKE -settle 2s"
    make chaos POLICY=prefix_affinity CHAOS_DIR=runs/box-check/chaos CHAOS_GPU=0 \
        SLO_FROM=runs/characterization CHAOS_RATE=4 \
        CHAOS_ARGS='-stop-cmd true -start-cmd true -duration 90s -warmup 10s -fault-at 20s -recover-at 45s -bucket 5s -sample-gpus=false'

The numbers those cells record are worthless — one fake replica answering on a fixed timer
is not a fleet, and every cell is flagged as having no cleanliness evidence. What they show
is that the targets run: `build` says it is running the cross-compiled binaries, the
suffixed binary starts, and cells and a recovery curve land on disk. Delete `runs/box-check`
afterwards. `LOAD_IMBALANCE=2` is on two of the lines because a sweep refuses to label a cell
with a spill point the router is not running: `make pressure-grid` always labels its cells
with the grid's own point, so the router has to be started at it, and then `make bench` has
to be told it too or it would label that same router's cells with no spill rule at all.

## The drivers

These scripts are committed as the exact files that produced a measurement, so a result can be traced
to the code that drove it. Each was checked byte for byte against the box's copy when it was
committed.

| script | what it does |
|---|---|
| `lib-sweep.sh` | The frozen comparison's parameters and the fleet and router lifecycle — `fleet_up`, `fleet_cycle`, `start_router`, `stop_router`. Sourced, not run. |
| `run-pressure-grid.sh` | #18's pressure grid: working set 0.25 / 1 / 3 / 8 × skew 0 / 1 / 1.4 at 32 users, one policy after another, the fleet cycled between them. |
| `run-pressure-rerun.sh` | Sets every flagged grid cell aside and re-runs it once on a cycled fleet, fills any incomplete point, then draws the map. Safe to run again. |
| `pressure-rerun-watch.sh` | Waits for the grid to finish, then starts the re-run pass — unless the grid died, in which case the grid itself should be resumed. |
| `run-pressure-spilloff.sh` | Prefix affinity with the spill rule off, across the same grid and the same bytes, into its own directory. |
| `run-exact-grid.sh` | #24's grid: exact residency against the approximate index, on a fleet publishing KV cache events, with session affinity as the baseline. |
| `run-hash-grid.sh` | #26's two arms: the stateless hash's weight axis, then the grid at the weight that axis settled on. |
| `run-recency-rerun.sh` | #29's two halves: the recency axis re-run at whole visit periods, and the WS 3 rung the working-set axis is missing. **Not yet run** — see below. |

`run-recency-rerun.sh` is committed before its run rather than after it, which is the
opposite of every other row here. What it encodes is a design that was settled off the GPU:
the cell lengths come from `internal/bench/recencywindow_test.go`, which derives them from
the workload's visit period and re-asserts that the longer cells still spread the axis the
run exists to plot. Committing it first is what lets that design be reviewed before the
fleet time is spent rather than after.

**It also carries a gate the other drivers here do not, and the reason is worth copying.**
Checking for a live `bench` or `router` before starting is not enough to tell whether
somebody else is on the cards: a sweep spends minutes between its cells cycling the fleet,
and in that window it has neither. A driver that looked only for those two would find the
box idle, and `fleet_up`'s first act is `fleet.sh down` — so it would take the other run's
fleet down at the one moment that run could not be seen. `run-recency-rerun.sh` therefore
looks for another *driver script*, which lives for the whole run, and it refuses on that.
Two more traps in the same family: arm the `trap cleanup EXIT` **after** the gates, or the
refusal itself tears the other fleet down on the way out; and `/tmp/kvroute-sweep.lock` is
not a convention you can rely on, because only `run-load-knee.sh` and `run-recency.sh` take
it. All three were found on 2026-09-12 by running the driver against a live `#31` sweep,
which it correctly declined.

Its flag set was rehearsed end to end against `cmd/fakereplica` with
`-cached-prompt-fraction` — replica, router, both drivers and `cmd/divergence` — so what is
unproven when it first meets the cards is the fleet, not the wiring. That rehearsal also
pinned the two settings a spill-off cell depends on now that #28 has added a second spill
condition: `hit_rate_low_water` and `load_imbalance_factor` both default to 0, so a router
started with neither flag is the spill-off policy #17 measured.

Launch a long run detached, with the `cd` separated by a semicolon so only the job is
backgrounded — `cd ~/kvroute && job &` backgrounds the whole chain, and its subshell holds
the ssh channel open for the life of the job:

    ssh nlp 'cd ~/kvroute; setsid nohup ./run-pressure-grid.sh > grid-console.log 2>&1 < /dev/null &'

#19's chaos drivers are committed too, as the evidence beside the measurement they
produced: `docs/measurements/2026-09-11-chaos-recovery/evidence/run-chaos.sh` and
`run-chaos-spilloff.sh`. They also source `lib-sweep.sh` from the box's root.

**`lib-sweep.sh` is shared.** The box's other drivers — `run-definitive.sh`,
`run-comparison.sh`, `run-divergence-ladder.sh` — source it too and are not in the repo, so
the box's copy is the live one and this is a snapshot of it (md5 `0564348afc86…`, unchanged
since 2026-09-08). If the two drift, the box's wins for anything run there.

**Never copy over one of these while it is running.** bash reads a script as it executes
it, so replacing a running script can corrupt the process mid-run. Write a new file beside
it instead — which is why the re-run pass is a separate script from the grid.
