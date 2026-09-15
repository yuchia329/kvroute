#!/usr/bin/env bash
# Re-run every flagged or incomplete cell of the pressure grid, then draw the map (#18).
#
#   ./run-pressure-rerun.sh           # set flagged cells aside, re-run them, draw the map
#   ./run-pressure-rerun.sh --list    # only list what would be re-run; touch nothing
#
# #18 criterion 5 and idea.md §6: unclean cells are excluded and RE-RUN rather
# than averaged in. The harness does that by itself only for contamination: a
# cell with a foreign process on a card is discarded and recomputed on resume
# (Contamination.Contaminated). A cell flagged for any other reason -- warm-up
# drift above all, which prefix affinity is the policy most exposed to -- is
# loaded on resume as "cached, not re-running" and stays excluded for good.
#
# So this does the re-run by hand, the way discard() does it for contamination:
# the record and rows move to <point>/discarded/<id>-<stamp>, outside cells/ so
# compaction never reads them as a result, and the next sweep of that point finds
# the cell missing and recomputes it. The point's other repetitions are cached
# and are not touched.
#
# Safe to run again. It also re-runs any point where a policy the grid reached
# has fewer than REPS repetitions on disk, which is exactly what a pass that died
# partway leaves behind: cells set aside and not yet recomputed. Without that, a
# second invocation would not see them -- they are no longer in cells/ to be
# found flagged -- and the grid would keep a silent hole.
#
# A separate file from run-pressure-grid.sh on purpose. bash reads a script as
# it executes it, so editing the grid runner while it runs can corrupt the live
# process; this is written and started beside it instead.
#
# Every setting below must match run-pressure-grid.sh. bench refuses a point
# whose cached cells offered a different workload, so a drift in geometry, WS or
# skew fails loudly -- but a drift in cell length would not, because the resume
# check does not compare it. That is why CELL is restated here and stays 300s.
source ./lib-sweep.sh

LOG=pressure-rerun.log
RUN=runs/pressure
# Its own evidence directory, because start_router writes router-<policy>.log
# there and the grid's own router logs must not be overwritten.
EVID=$RUN/evidence/rerun
mkdir -p "$EVID"

# --- Must match run-pressure-grid.sh -----------------------------------------
CONC=32
CELL=300s
WARM=50s
REPS=3
KV_CAPACITY=629760
GRID_GEOMETRY="-workload multiturn -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -seed 1 \
 -kv-capacity $KV_CAPACITY"
LOAD_IMBALANCE=2
SPILL_LABEL="-spill 0/$LOAD_IMBALANCE"
SPILL_ROUTER="-load-imbalance-factor $LOAD_IMBALANCE"
GRID_CAL=$DERIVED

# The same order the grid ran the policies in.
POLICY_ORDER=(session_affinity prefix_affinity round_robin least_outstanding)

# fleet_up sets these, and this script cycles the fleet rather than bringing it
# up from nothing. They are pure configuration, so they are read directly.
SPECS="$(./ops/fleet.sh replicas)"
MODEL="$(./ops/fleet.sh env MODEL)"
GPUS="$(./ops/fleet.sh env REPLICA_GPUS)"

# flagged_cells prints one line per flagged cell:
#   policy <TAB> ws <TAB> skew <TAB> id <TAB> point <TAB> reasons
#
# ws and skew are taken from the point's DIRECTORY NAME, not from the cell
# record. The record holds them as floats, so WS 1 would come back as 1.0, and
# the re-run would sweep into ws1.0-skew0 -- a new directory the cell's own
# repetitions are not in. The directory name is the exact string the grid used.
flagged_cells() {
  python3 - <<'PY'
import glob, json, os
for path in sorted(glob.glob("runs/pressure/ws*-skew*/cells/*.json")):
    try:
        cell = json.load(open(path))
    except Exception:
        continue  # a truncated record is not a cached cell; bench re-runs it anyway
    summary = cell.get("summary", {})
    if not summary.get("flagged"):
        continue
    point = os.path.basename(os.path.dirname(os.path.dirname(path)))
    ws, skew = point[len("ws"):].split("-skew")
    reasons = "; ".join(summary.get("flag_reasons") or ["no reason recorded"])
    print("\t".join([cell["policy"], ws, skew, cell["id"], point, reasons]))
PY
}

# incomplete_points prints policy <TAB> ws <TAB> skew for every point where a
# policy the grid reached has fewer than REPS repetitions in cells/.
#
# "Reached" is judged by the policy having a cell there OR one set aside in
# discarded/. A policy with neither never ran that point, which is not a hole
# this pass should fill: it is a grid still running, or a policy not yet swept.
incomplete_points() {
  REPS="$REPS" python3 - <<'PY'
import glob, os
reps = int(os.environ["REPS"])
policies = ["session_affinity", "prefix_affinity", "round_robin", "least_outstanding"]
for point in sorted(glob.glob("runs/pressure/ws*-skew*")):
    name = os.path.basename(point)
    ws, skew = name[len("ws"):].split("-skew")
    for policy in policies:
        have = len(glob.glob(f"{point}/cells/{policy}-c32-r*.json"))
        gone = len(glob.glob(f"{point}/discarded/{policy}-c32-r*.json"))
        if have < reps and (have > 0 or gone > 0):
            print("\t".join([policy, ws, skew]))
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

rerun_point() {
  local policy="$1" ws="$2" skew="$3"
  local spill=""
  [[ "$policy" == "prefix_affinity" ]] && spill="$SPILL_LABEL"
  say "$policy: re-running WS $ws skew $skew"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$RUN/ws$ws-skew$skew" \
    -policy "$policy" $spill \
    -concurrency "$CONC" \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$REPS" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GRID_GEOMETRY -working-set "$ws" -skew "$skew" \
    >> "$LOG" 2>&1 || fatal "$policy re-run at WS $ws skew $skew failed; see $LOG"
}

report() {
  local label="$1"; shift
  local lines=("$@")
  if [[ ${#lines[@]} -eq 0 ]]; then
    say "$label: none"
    return
  fi
  say "$label: ${#lines[@]}"
  for line in "${lines[@]}"; do say "  ${line//$'\t'/  }"; done
}

# --- Entry --------------------------------------------------------------------
mapfile -t FLAGGED < <(flagged_cells)
mapfile -t INCOMPLETE < <(incomplete_points)
report "flagged cells (policy ws skew id point reasons)" "${FLAGGED[@]}"
report "incomplete points (policy ws skew)" "${INCOMPLETE[@]}"

if [[ "${1:-}" == "--list" ]]; then
  exit 0
fi

for policy in "${POLICY_ORDER[@]}"; do
  points=()
  for line in "${FLAGGED[@]}"; do
    IFS=$'\t' read -r p ws skew id point reasons <<< "$line"
    [[ "$p" == "$policy" ]] || continue
    set_aside "$point" "$id"
    # One sweep per point re-runs every repetition missing there.
    [[ " ${points[*]:-} " == *" $ws/$skew "* ]] || points+=("$ws/$skew")
  done
  for line in "${INCOMPLETE[@]}"; do
    IFS=$'\t' read -r p ws skew <<< "$line"
    [[ "$p" == "$policy" ]] || continue
    [[ " ${points[*]:-} " == *" $ws/$skew "* ]] || points+=("$ws/$skew")
  done
  [[ ${#points[@]} -eq 0 ]] && continue

  # A cold fleet per policy, for the reason the grid cycles it: every point sends
  # every policy the same bytes, so a warm fleet would hand this policy the
  # previous one's blocks.
  fleet_cycle
  ./ops/probe-usage.sh >> "$LOG" 2>&1 \
    || fatal "after the restart the fleet does not report cached prompt tokens; see $LOG"

  router_args=""
  [[ "$policy" == "prefix_affinity" ]] && router_args="-prefix-calibration $GRID_CAL $SPILL_ROUTER"
  start_router "$policy" "$RUN/router-rerun-$policy.jsonl" $router_args
  for wsskew in "${points[@]}"; do
    rerun_point "$policy" "${wsskew%/*}" "${wsskew#*/}"
  done
  stop_router
done

# One pass, not a loop. A cell that drifts again on re-run is reported rather
# than retried without end: each attempt costs a fleet cycle and five minutes of
# GPU, and a cell that will not settle is itself a finding about that point.
mapfile -t STILL_FLAGGED < <(flagged_cells)
mapfile -t STILL_INCOMPLETE < <(incomplete_points)
report "still flagged after re-running -- reported, not retried" "${STILL_FLAGGED[@]}"
report "still incomplete after re-running" "${STILL_INCOMPLETE[@]}"

# The map. pressuremap exits non-zero on exactly one verdict -- nothing separated
# and the mechanism never fired -- which is a finding to read, not a crash, so it
# does not stop this script.
./bin/pressuremap-linux-amd64 -out runs/pressuremap.md "$RUN"/ws*-skew* >> "$LOG" 2>&1
say "pressure map written to runs/pressuremap.md (pressuremap exit $?)"
say "=== pressure grid re-run pass complete ==="
