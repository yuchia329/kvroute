#!/usr/bin/env bash
# Waits for the pressure grid to finish, then starts the re-run pass (#18).
#
#   setsid ./pressure-rerun-watch.sh > rerun-console.log 2>&1 < /dev/null &
#
# It exists so the grid, its re-runs and the map all land without anyone awake:
# the grid runner cannot be edited to do this itself while it is running, because
# bash reads a script as it executes it.
#
# It will not start the re-run pass on top of a grid that died. A FATAL means the
# grid stopped partway, and filling its flagged cells then would re-run a handful
# of repetitions against a grid that is missing whole points -- the thing to do
# there is resume the grid itself, which bench makes cheap.
cd ~/kvroute || exit 1

say() { echo "[$(date -u +%H:%M:%S)] $*"; }

say "watching grid-console.log for the grid to finish"
until grep -qE "pressure grid complete|FATAL" grid-console.log 2>/dev/null; do
  sleep 60
done

if grep -q "FATAL" grid-console.log; then
  say "the grid ended in FATAL; not starting the re-run pass. Resume the grid instead:"
  grep "FATAL" grid-console.log
  exit 1
fi

say "grid complete; starting the re-run pass"
exec ./run-pressure-rerun.sh
