#!/usr/bin/env bash
# #26 on the box: the stateless prefix hash, first swept along its own weight
# axis and then run across the pressure grid beside the policies that track
# residency.
#
#   ./run-hash-grid.sh smoke      # one short cell: the window fits, the hash decides
#   ./run-hash-grid.sh weights    # the weight axis, 5 points, ~1.7 h
#   ./run-hash-grid.sh grid       # the grid arm at $WEIGHT, 12 points, ~3.1 h
#
# The smoke is sized to borrow a slot rather than own one. Bringing this fleet up
# is six serialized replica starts, about a minute each, so a smoke that insisted
# on its own fleet would need twelve minutes to answer a question worth sixty
# seconds. Run it while a fleet is already up -- between two other tickets' runs --
# and it starts a router, sends one 60 s cell and stops, leaving the fleet exactly
# as it found it. With no fleet up it brings one up and takes it down again.
#
# The policy holds no index and no session state: it hashes the prompt's leading
# blocks onto a ring of the replicas and weighs that ranking against inflight.
# It is the control that isolates what the prefix index buys — with #24's exact
# residency above it the grid reads as a ladder of how much a router knows about
# what its replicas hold: none, believed, exact.
#
# It is NOT a reproduction of OpenAI's router, and nothing this run produces may
# be reported as one. Their documentation says they route by "a hash of the
# initial tokens" plus machine load and that prompt_cache_key "influences
# routing"; the window, the weighting and the placement here are all choices made
# in this repo, because no number for any of them has been published.
#
# weights and grid run on a fleet publishing its KV cache events -- KV_EVENTS="1"
# in ops/versions.env -- and refuse to start on one that is not. Not because this
# policy reads them: it does not. Because the grid arm's cells land beside #24's
# in runs/pressure-kv-events, publishing is work the engine does on every step
# (ADR-0010), and cells recorded under the two settings are two measurements
# rather than one comparison. The weight sweep runs under the same fleet for the
# same reason: a weight settled on one engine configuration and applied on
# another was settled somewhere else.
#
# The smoke does not check the setting and must not. Its cell goes to a directory
# of its own and can never join a grid, it is asking whether the router reads this
# workload's prompts rather than what the fleet scores, and requiring the setting
# would mean it could only run on a fleet configured for somebody else's night.
#
# Resumable, as the other runners are: cells on disk are loaded rather than
# re-run. It leaves the fleet down however it ends, because the box is shared.
source ./lib-sweep.sh
set -o pipefail

LOG=hash-grid.log
RUN=runs/pressure-kv-events
WEIGHTS_RUN=runs/hash-weight
SMOKE=runs/hash-smoke
EVID=$RUN/evidence
mkdir -p "$EVID" "$SMOKE" "$WEIGHTS_RUN"

# --- The grid: #18's and #24's, to the value ----------------------------------
WORKING_SETS=(0.25 1 3 8)
SKEWS=(0 1 1.4)
CONC=32
CELL=300s
WARM=50s
REPS=3
KV_CAPACITY=629760
GRID_GEOMETRY="-workload multiturn -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -seed 1 \
 -kv-capacity $KV_CAPACITY"
POLICY=prefix_hash

# --- The grid point: stated, never defaulted ----------------------------------
# BLOCKS is bench.HashLeadingBlocks: 16 blocks of 64 bytes, 1,024 bytes of
# prompt, which is past the 512-byte shared system prompt that a third of the
# sessions send identically and well inside the ~1,792 bytes a first turn
# contributes and every later turn resends unchanged. Both ends are the
# workload's geometry; a test pins them to it.
#
# WEIGHT is what one step down the hash's ranking is worth in inflight requests,
# and it is the axis `weights` sweeps. The grid arm runs at one point of it, and
# that point is an outcome of the sweep: run `weights` first, read the goodput
# and the deflection rate off its cells, and set WEIGHT here before running
# `grid`.
BLOCKS=16
WEIGHT=${WEIGHT:-4}
WEIGHT_GRID=(0 1 4 12 32)

# The smoke's own geometry. Sixty seconds against the grid's three hundred,
# because it is checking that prompts fill the window and the ranking decides
# something -- both of which the first few hundred requests settle -- and not
# measuring goodput. Its cells are in $SMOKE and are never compared with
# anything.
SMOKE_CELL=60s
SMOKE_WARM=20s

# The weight axis runs at the load point, WS 1 / skew 1.4 — where conversations
# pile up and the balance between the two terms is live. At skew 0 the hash lands
# traffic evenly by construction and every weight reads the same.
AXIS_WS=1
AXIS_SKEW=1.4

RPID=""
# BORROWED says the fleet was already up when this started, in which case it is
# somebody else's and is left running. Taking down a fleet another ticket is
# mid-run on would cost them the night, which is a worse failure than this script
# not running at all.
BORROWED=0
cleanup() {
  if [[ -n "$RPID" ]]; then kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; fi
  if (( BORROWED )); then
    say "fleet left up: it was already running when this started"
    return
  fi
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "fleet down; GPUs released"
}
trap cleanup EXIT

# fleet_running reports whether every replica this box defines is up and
# answering. Both halves matter: a PID that is alive during a bring-up is not a
# replica that can serve a request, and half a fleet is not a fleet to borrow.
fleet_running() {
  local specs id url
  specs="$(./ops/fleet.sh replicas 2>/dev/null)" || return 1
  [[ -n "$specs" ]] || return 1
  ./ops/fleet.sh status 2>/dev/null | grep -q "running" || return 1
  for spec in ${specs//,/ }; do
    id="${spec%%=*}"; url="${spec#*=}"
    curl -sf --max-time 2 "$url/health" >/dev/null || return 1
  done
  return 0
}

# replicas_alive is how many replica processes this box currently has running,
# whether or not they are answering yet.
replicas_alive() { ./ops/fleet.sh status 2>/dev/null | grep -c "running" || true; }

# take_fleet borrows a fleet that is already up, or brings one up and owns it,
# and refuses the state between the two.
#
# The refusal is the important branch. fleet_up starts by taking the fleet down,
# before preflight can object to anything, so a script that treated "not all
# healthy" as "nothing is running" would kill another ticket's replicas — and
# this fleet comes up one replica at a time, about a minute each, so "some
# processes running, not all answering" is exactly what somebody else's bring-up
# looks like for six minutes. There is no way to tell that from a fleet whose
# replicas are being restarted, so neither is touched.
take_fleet() {
  if fleet_running; then
    BORROWED=1
    SPECS="$(./ops/fleet.sh replicas)"
    MODEL="$(./ops/fleet.sh env MODEL)"
    GPUS="$(./ops/fleet.sh env REPLICA_GPUS)"
    say "borrowing the fleet that is already up on GPUs $GPUS; it will be left running"
    return
  fi
  local alive; alive="$(replicas_alive)"
  if (( alive > 0 )); then
    fatal "$alive replica process(es) are running but the fleet is not answering on every one of them. That is what another ticket's bring-up looks like while it is happening, and bringing up here would take their replicas down first. Wait for them, or stop the fleet yourself and re-run"
  fi
  fleet_up
}

# run_point runs one cell point. The window and the weight are spelled to both
# the router and the harness; bench refuses to start when the two disagree, which
# is the whole reason they are spelled twice rather than inferred once.
run_point() {
  local base="$1" weight="$2" ws="$3" skew="$4" reps="$5"
  local cell="$CELL" warm="$WARM" events="true"
  if [[ "$base" == "$SMOKE" ]]; then
    cell="$SMOKE_CELL"; warm="$SMOKE_WARM"
    # Recorded as whatever the fleet it borrowed is actually doing, rather than
    # asserted: a smoke that labelled itself events-on while running on a fleet
    # that publishes nothing would be the one lie this script exists to catch.
    events="$([[ "$(./ops/fleet.sh env KV_EVENTS)" == "1" ]] && echo true || echo false)"
  fi
  say "$POLICY at ${BLOCKS}/${weight}: WS $ws skew $skew ($reps reps, $cell cells)"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$base/ws$ws-skew$skew" \
    -policy "$POLICY" -hash "$BLOCKS/$weight" -fleet-kv-events="$events" \
    -concurrency "$CONC" \
    -cell-duration $cell -warmup $warm -settle $SETTLE \
    -repetitions "$reps" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GRID_GEOMETRY -working-set "$ws" -skew "$skew" \
    >> "$LOG" 2>&1 || fatal "$POLICY at ${BLOCKS}/${weight}, WS $ws skew $skew failed; see $LOG"
}

# hash_report prints how the run's decisions split between the hash's own
# ranking and the load term that moved requests off it. That split is what the
# weight axis is read by: a weight that deflected nothing and one that deflected
# everything are two different policies wearing one name.
hash_report() {
  local cell="$1"
  python3 - "$cell" <<'PY' | tee -a "$LOG"
import json, sys
c = json.load(open(sys.argv[1]))
d = c["summary"]["decisions"]
total = sum(v for v in d.values() if isinstance(v, int))
share = (lambda n: 0.0 if total == 0 else 100.0 * n / total)
print("  decisions: prefix_hash %d (%.1f%%)  hash_deflected %d (%.1f%%)  prompt_unhashed %d  undecided %d" % (
    d["prefix_hash"], share(d["prefix_hash"]),
    d["hash_deflected"], share(d["hash_deflected"]),
    d["prompt_unhashed"], d["undecided"]))
print("  goodput %.2f/s  p90 TTFT %.0f ms  flagged %s  %s" % (
    c["summary"]["goodput_rps"], c["summary"]["ttft_p90_ns"] / 1e6,
    c["summary"]["flagged"], c["summary"].get("flag_reasons", "")))
PY
}

# --- Entry --------------------------------------------------------------------
MODE="${1:-grid}"
say "=== stateless prefix hash (#26): $MODE ==="

if [[ "$MODE" == "smoke" ]]; then
  # Borrow whatever is up. No events gate and no usage probe: this cell is never
  # compared with anything, and both checks are about a grid it will not join.
  take_fleet
else
  [[ "$(./ops/fleet.sh env KV_EVENTS)" == "1" ]] \
    || fatal "KV_EVENTS is not 1 in ops/versions.env, so these cells could not be compared with #24's. Set it there -- not in the shell -- and re-run"
  fleet_up
  ./ops/probe-usage.sh >> "$LOG" 2>&1 \
    || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"
  say "usage probe passed: the engines report cached prompt tokens"
fi

case "$MODE" in
  smoke)
    # One point, into a directory of its own so it can never stand in for a grid
    # cell. It proves the two things the nights depend on: the window fits the
    # prompts this workload sends, and the hash actually decides something.
    start_router "$POLICY" "$SMOKE/router.jsonl" -hash-leading-blocks "$BLOCKS" -hash-weight "$WEIGHT"
    run_point "$SMOKE" "$WEIGHT" "$AXIS_WS" "$AXIS_SKEW" 1
    stop_router; RPID=""
    cell="$SMOKE/ws$AXIS_WS-skew$AXIS_SKEW/cells/$POLICY-c$CONC-r1.json"
    hash_report "$cell"
    python3 - "$cell" <<'PY' | tee -a "$LOG"
import json, sys
c = json.load(open(sys.argv[1]))
d = c["summary"]["decisions"]
ok = d["prefix_hash"] > 0 and d["prompt_unhashed"] == 0 and d["undecided"] == 0
print("  SMOKE " + ("PASSED" if ok else
      "FAILED: a window no prompt filled, a hash that decided nothing, or a reason this harness does not know"))
sys.exit(0 if ok else 1)
PY
    [[ $? -eq 0 ]] || fatal "smoke failed; see $LOG"
    say "=== smoke passed ==="
    ;;

  weights)
    # Each weight into its own directory. A cell id names the policy, the load
    # level and the repetition and nothing about the weight, so two weights swept
    # into one directory would resume each other -- bench refuses that now, and
    # this layout is what keeps it from coming up.
    #
    # The fleet is cycled between weights, not just the router. Every weight
    # sends identical bytes at this point, so the second would read the caches
    # the first left warm and measure the fleet's memory rather than its own
    # routing (ADR-0004).
    for weight in "${WEIGHT_GRID[@]}"; do
      fleet_cycle
      start_router "$POLICY" "$WEIGHTS_RUN/router-w$weight.jsonl" \
        -hash-leading-blocks "$BLOCKS" -hash-weight "$weight"
      run_point "$WEIGHTS_RUN/w$weight" "$weight" "$AXIS_WS" "$AXIS_SKEW" "$REPS"
      stop_router; RPID=""
      hash_report "$WEIGHTS_RUN/w$weight/ws$AXIS_WS-skew$AXIS_SKEW/cells/$POLICY-c$CONC-r1.json"
    done
    say "=== weight axis complete: $WEIGHTS_RUN ==="
    say "Read goodput and the deflection rate off the cells, set WEIGHT, then run: $0 grid"
    ;;

  grid)
    started=$(date +%s)
    fleet_cycle
    start_router "$POLICY" "$RUN/router-$POLICY.jsonl" \
      -hash-leading-blocks "$BLOCKS" -hash-weight "$WEIGHT"
    for ws in "${WORKING_SETS[@]}"; do
      for skew in "${SKEWS[@]}"; do
        run_point "$RUN" "$WEIGHT" "$ws" "$skew" "$REPS"
      done
    done
    stop_router; RPID=""
    elapsed=$(( $(date +%s) - started ))
    say "$POLICY: all 12 points done in $((elapsed/3600))h$(( (elapsed%3600)/60 ))m"
    say "=== grid arm complete ==="
    # The ladder, drawn as the pair it is about: what tracking belief buys over a
    # stateless prefix hash that tracks nothing.
    ./bin/pressuremap-linux-amd64 -out runs/pressuremap-hash.md \
      -baseline "$POLICY" -challenger prefix_affinity "$RUN"/ws*-skew* >> "$LOG" 2>&1 \
      || say "pressuremap exited non-zero (see $LOG): read the map's validity section before anything else"
    say "map: runs/pressuremap-hash.md"
    ;;

  *)
    fatal "unknown mode $MODE: one of smoke, weights, grid"
    ;;
esac
