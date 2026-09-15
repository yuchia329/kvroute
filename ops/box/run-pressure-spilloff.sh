#!/usr/bin/env bash
# Prefix affinity with the spill rule OFF, across the whole pressure grid (#18).
#
#   ./run-pressure-spilloff.sh
#
# The grid found prefix affinity beating session affinity under skew by
# balancing load: it serves each hot conversation from every replica that holds
# it, choosing among them by load. Spill fired on only 0.2-2.5% of its
# decisions, so the balancing looked like tie-breaking. What the grid cannot say
# is whether those rare spills are what put a hot conversation onto a second
# replica in the first place -- in which case spill is necessary to the
# mechanism however seldom it fires. This arm answers it: the same policy, the
# same bytes, the same fleet and the same binaries, with the spill rule off. If
# prefix affinity still spreads hot conversations and holds its goodput,
# tie-breaking alone does it. If it collapses toward session affinity's pile-up,
# the spills were seeding it.
#
# Its own directory, runs/pressure-spilloff, and that is not a nicety. A cell id
# is policy-load-repetition and carries no spill setting, and resume does not
# compare spill, so running this into runs/pressure would find the spill-on
# prefix_affinity cells there, report them cached, and run nothing at all.
#
# The same bytes as the spill-on arm, on purpose. A cell's slice of the
# workload's user space comes from its load, repetition and grid point, none of
# which differ, so each spill-off cell sends exactly what its spill-on twin sent
# -- which is what makes the two arms comparable cell by cell. The fleet starts
# cold (fleet_up), so nothing from the spill-on run is still cached.
#
# It re-runs its own flagged cells once, the way run-pressure-rerun.sh does, and
# brings the fleet down when it finishes: the box is shared.
source ./lib-sweep.sh

LOG=pressure-spilloff.log
RUN=runs/pressure-spilloff
EVID=$RUN/evidence
mkdir -p "$EVID"

# --- Must match run-pressure-grid.sh in everything but the spill rule ---------
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
GRID_CAL=$DERIVED
POLICY=prefix_affinity

# No -spill label and no spill flags on the router: both default to off, which
# is policy 4 as #15 defined it. bench checks the label against the router
# before the first cell, so the two cannot silently disagree.
run_point() {
  local ws="$1" skew="$2"
  say "$POLICY spill off: WS $ws skew $skew ($REPS reps)"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$RUN/ws$ws-skew$skew" \
    -policy "$POLICY" \
    -concurrency "$CONC" \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$REPS" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GRID_GEOMETRY -working-set "$ws" -skew "$skew" \
    >> "$LOG" 2>&1 || fatal "$POLICY spill off at WS $ws skew $skew failed; see $LOG"
}

# flagged_cells prints point <TAB> id <TAB> reasons for each flagged cell.
flagged_cells() {
  python3 - "$RUN" <<'PY'
import glob, json, os, sys
for path in sorted(glob.glob(os.path.join(sys.argv[1], "ws*-skew*", "cells", "*.json"))):
    try:
        cell = json.load(open(path))
    except Exception:
        continue  # a truncated record is not a cached cell; bench re-runs it anyway
    summary = cell.get("summary", {})
    if not summary.get("flagged"):
        continue
    point = os.path.basename(os.path.dirname(os.path.dirname(path)))
    print("\t".join([point, cell["id"], "; ".join(summary.get("flag_reasons") or ["no reason recorded"])]))
PY
}

# set_aside mirrors bench's discard(): record and rows out of cells/, stamped.
set_aside() {
  local point="$1" id="$2" stamp
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$RUN/$point/discarded"
  for ext in json jsonl; do
    if [[ -f "$RUN/$point/cells/$id.$ext" ]]; then
      mv "$RUN/$point/cells/$id.$ext" "$RUN/$point/discarded/$id-$stamp.$ext"
    fi
  done
}

# --- Entry --------------------------------------------------------------------
say "=== prefix affinity, spill off: starting ==="
fleet_up
./ops/probe-usage.sh >> "$LOG" 2>&1 \
  || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"
say "usage probe passed: the engines report cached prompt tokens"

start_router "$POLICY" "$RUN/router-$POLICY-spilloff.jsonl" -prefix-calibration "$GRID_CAL"
for ws in "${WORKING_SETS[@]}"; do
  for skew in "${SKEWS[@]}"; do
    run_point "$ws" "$skew"
  done
done
stop_router
say "$POLICY spill off: all 12 points done"

# One re-run pass, not a loop, for the reason run-pressure-rerun.sh gives: a
# cell that will not settle on a second attempt is a finding about its point.
mapfile -t FLAGGED < <(flagged_cells)
if [[ ${#FLAGGED[@]} -eq 0 ]]; then
  say "no flagged cells"
else
  say "${#FLAGGED[@]} flagged cell(s): setting aside and re-running once"
  points=()
  for line in "${FLAGGED[@]}"; do
    IFS=$'\t' read -r point id reasons <<< "$line"
    say "  $point  $id  -- $reasons"
    set_aside "$point" "$id"
    [[ " ${points[*]:-} " == *" $point "* ]] || points+=("$point")
  done

  # Cold again, so a re-run point does not inherit the cache state the rest of
  # the arm left behind; its own evidence directory, so the arm's router log is
  # not overwritten.
  fleet_cycle
  ./ops/probe-usage.sh >> "$LOG" 2>&1 \
    || fatal "after the restart the fleet does not report cached prompt tokens; see $LOG"
  EVID=$RUN/evidence/rerun
  mkdir -p "$EVID"
  start_router "$POLICY" "$RUN/router-$POLICY-spilloff-rerun.jsonl" -prefix-calibration "$GRID_CAL"
  for point in "${points[@]}"; do
    ws="${point#ws}"; ws="${ws%%-skew*}"
    skew="${point##*-skew}"
    run_point "$ws" "$skew"
  done
  stop_router

  mapfile -t STILL < <(flagged_cells)
  say "still flagged after re-running -- reported, not retried: ${#STILL[@]}"
  for line in "${STILL[@]}"; do say "  ${line//$'\t'/  }"; done
fi

./ops/fleet.sh down >> "$LOG" 2>&1 || true
say "fleet down: GPUs released"
say "=== prefix affinity, spill off: complete ==="
