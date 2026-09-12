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
