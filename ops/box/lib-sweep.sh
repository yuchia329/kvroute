# The frozen parameter set for the four-policy comparison. Sourced, not run.
#
# Every value here is frozen. Changing any of them invalidates every cell already
# recorded under them, and forces a re-measurement of every policy -- which has
# already happened three times in this project:
#
#   1. the fixed workload had no shared prefix, so it could not measure a
#      cache-aware policy at all
#   2. the arrival rotation was shuffled after the open-loop cells were recorded
#   3. (avoided) the bytes-per-token label is wrong, and correcting it would
#      change the workload name and refuse every existing cell against it
#
# Four more policies are still to be measured -- #16's spill grid, #24's exact
# residency, #26's stateless prefix hash -- and each lands in this same table. If
# these values move, every one of them takes all the others down with it.
#
# compare hard-checks the first three: workload name, SLO, arrival plan. It
# refuses rather than mixing them. The rest are checked by nobody and change the
# numbers silently, which is why they are written down here rather than typed per
# run.
set -u
cd ~/kvroute
export PATH="$HOME/.local/bin:$PATH"

# --- Frozen: hard-checked by compare ---------------------------------------
# The SLO comes from the 2026-09-07 characterization and is NOT re-derived. It
# was measured on the six-card fleet, before GPU 3 was dropped; the floor is a
# solo measurement and GPU 3 only throttles under simultaneous load, so it stands.
# Re-deriving it on five cards would change every goodput figure ever recorded.
SLO=slo/characterization.json

# bpt=4 is the generator's assumption and the engines actually report 1.66, so
# "working set 1.0" is really nearer 2.5. It is wrong identically for all four
# policies, so the comparison is sound and the label is not. Do not correct it:
# the ratio is part of the workload name, and changing it refuses every cell.
WORKLOAD="-workload multiturn -sessions 307 -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -skew 0 -seed 1"

# --- Frozen: silently change the numbers, checked by nobody ------------------
# 150s cells with a 50s warm-up leave a 100s measured window, against the 65s the
# 2026-09-08 cells had. The longer warm-up is not a luxury: prefix affinity's
# index starts empty and fills during the cell, and every cell deliberately sends
# prompts the fleet has not seen (ADR-0004), so it cannot be pre-warmed across
# cells. At 128 users all three of its cells were discarded for warm-up drift
# while none of session affinity's were -- the check penalises the only policy
# with something to learn, and #16, #24 and #26 will all hit it too.
CELL=150s; WARM=50s; SETTLE=10s; REPS=3

# Spill stays off. Both router flags default to 0, which is prefix affinity as
# #15 measured it; #16's grid is a separate set of cells carrying its own label.
SPILL_ARGS=""

# The index's node cap is frozen as fleet-sized: capacity x measured
# bytes-per-token / 64, recomputed off the live fleet at calibration time. An
# offline replay of the recorded rows shows it binds at 100% occupancy at every
# load from 64 users up, so it is not an implementation detail -- it shapes
# routing, and it is therefore part of what policy 4 IS. #17's divergence
# calibration is reported as a second configuration against this one rather than
# replacing it, which is what keeps this run from going stale.
CAL=runs/prefix-calibration.json
DERIVED=runs/prefix-calibration-derived.json

say() { echo "[$(date -u +%H:%M:%S)] $*"; }
fatal() { say "FATAL: $*"; exit 1; }

# Resumable: a run that died leaves the fleet up, and preflight would then refuse
# to start. Taking it down first is harmless when it is already down.
fleet_up() {
  ./ops/fleet.sh down >> "$LOG" 2>&1 || true
  sleep 5
  ./ops/fleet.sh preflight >> "$LOG" 2>&1 || fatal "preflight failed: a GPU outside this fleet holds memory"
  ./ops/fleet.sh up >> "$LOG" 2>&1 || fatal "fleet did not come up"
  SPECS="$(./ops/fleet.sh replicas)"
  MODEL="$(./ops/fleet.sh env MODEL)"
  GPUS="$(./ops/fleet.sh env REPLICA_GPUS)"
  say "fleet is up on GPUs $GPUS"
}

# ADR-0004: every policy sends identical bytes at each load point, so the next
# pass would read the caches this one left warm. Down and up drops them.
fleet_cycle() {
  say "restarting the fleet before the next policy (ADR-0004)"
  ./ops/fleet.sh down >> "$LOG" 2>&1
  sleep 20
  ./ops/fleet.sh up >> "$LOG" 2>&1 || fatal "fleet did not come back up"
  say "fleet is back up"
}

start_router() {
  local policy="$1" records="$2"; shift 2
  ./bin/router-linux-amd64 -listen 127.0.0.1:8080 -replicas "$SPECS" -policy "$policy" \
    -records "$records" "$@" > "$EVID/router-$policy.log" 2>&1 &
  RPID=$!
  sleep 3
  curl -sf http://127.0.0.1:8080/healthz > /dev/null \
    || { tail -20 "$EVID/router-$policy.log"; fatal "router did not come up for $policy"; }
  say "$policy: router up"
}

stop_router() { kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; say "router drained"; }
