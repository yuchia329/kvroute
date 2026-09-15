#!/usr/bin/env bash
# The residency branch armed across the whole pressure grid, at all three of its
# levels (#18's separability criterion, and the run that prices #28's mark).
#
#   ./run-pressure-residency.sh
#
# #18's second criterion is that memory pressure and load imbalance are
# separable in the results, "since they drive different branches of the spill
# rule". The grid could not answer it: through #16 the residency branch read
# vllm:kv_cache_usage_perc, which counts the blocks held by a replica's *running*
# requests, so it read the active batch the load branch already reads -- r = 0.973
# over 13,658 decisions. bench.Chosen therefore kept that branch OFF, the grid ran
# load-only, and its residency column is zeros by construction. The map says so
# rather than printing them as a finding.
#
# #28 replaced the signal with the engines' own per-replica prefix cache hit rate
# over a moving window, and measured it separable from load on the same rows:
# r = 0.138 against the gauge's 0.979, moving 0.6 points across the whole load
# range where the gauge moves 47.3 (ADR-0011). What it did not do is sweep the
# axis, so bench.Chosen keeps HitRateLowWater: 0 and #18's criterion stayed
# evaluable but unmeasured. This arm measures it.
#
# The arm is the mark ALONE -- LoadImbalanceFactor 0 at every cell, so nothing
# but the residency mark can decline a match, and its column is that branch and
# no other. Read beside the two arms already recorded on the same twelve points
# and the same bytes:
#
#   spill off   0/0        runs/pressure-spilloff   (#18's mechanism arm)
#   load only   0/2        runs/pressure            (the grid, bench.Chosen)
#   residency   0.55/0     here
#               0.62/0
#               0.70/0
#
# That is a five-way comparison at every point of the grid, which prices the
# residency axis where #28 left it unpriced and answers the separability question
# directly: if the branches are separable, the residency column climbs with
# working set -- eviction is what it measures -- while the load column climbs
# with skew. The grid's load column already falls down the working set axis
# (956/510/348/272), so the two are not expected to agree.
#
# Three levels rather than one because the answer is a curve and one point is a
# guess on it: 0.55/0.62/0.70 are bench.HitRateLowWaterGrid, cut against the
# signal's own observed range (p10 0.560, p50 0.662, p90 0.735) by how often each
# declines a match AND finds anywhere to send it -- 5.2%, 23.3%, 42.5% effective.
# Their comment carries the distribution; do not retype the levels here.
#
# A mark per directory, and that is not a nicety. A cell id is
# policy-load-repetition and carries no spill setting, and resume does not
# compare spill, so a second mark run into the first one's directory would find
# its cells, report them cached, run nothing, and write a table describing the
# mark it replaced. #28 hit exactly this between its honoured-rate and hit-rate
# passes. Separate directories, plus a stamp per directory checked on entry.
#
# Same bytes as both existing arms, cell for cell. A cell's slice of the
# workload's user space comes from its load, repetition and grid point, none of
# which differ here, and the geometry below is the grid's. The fleet is cycled
# between marks, so no mark reads the cache its predecessor left warm (ADR-0004).
#
# Roughly 10.5 hours: 3 marks x 12 points x 3 reps x 310 s, plus a fleet cycle
# and a re-run pass per mark. The spill-off arm, one third of this, took 3h22m.
#
# It re-runs its own flagged cells once per mark, the way run-pressure-spilloff.sh
# does, and brings the fleet down when it finishes: the box is shared.
#
#   ./run-pressure-residency.sh smoke
#
# proves the pipeline before committing the night: the guards, the router flag
# and bench's check of the label against it, on one point at one mark. It runs a
# SHORT cell into a directory of its own, runs/pressure-residency-smoke, for the
# reason #18's own smoke exists as a cautionary tale -- a cell id carries no
# geometry, so a 150 s smoke cell run into the grid's directory was resumed as
# repetition 1 of a 300 s point and pooled a half-length window into its median.
# bench refuses that now (the cell records its length and warm-up), and the smoke
# still keeps its own directory: a refusal on the first cell of the night is a
# night spent refusing.
source ./lib-sweep.sh

LOG=pressure-residency.log
RUN=runs/pressure-residency
mkdir -p "$RUN"

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

# bench.HitRateLowWaterGrid (9588ba3). Lightest first, so the cheapest arm is on
# disk first if the box is taken back before the run finishes.
MARKS=(0.55 0.62 0.70)

# --- Guards -------------------------------------------------------------------
# KV cache events must be OFF, as they were for the grid and the spill-off arm.
# #24 left the box's ops/versions.env at KV_EVENTS=1 once (backup at
# /tmp/versions.env.bak-24), and a fleet publishing events is a different engine
# configuration from one that is not (ADR-0007, ADR-0010). The failure is silent:
# the arm would finish and its cells would carry a label matching nothing they
# are meant to be read beside.
require_events_off() {
  local events
  events="$(./ops/fleet.sh env KV_EVENTS)" || fatal "could not read KV_EVENTS"
  [ "$events" = "0" ] || fatal "ops/versions.env has KV_EVENTS=$events; the grid and the spill-off arm are events-off (see /tmp/versions.env.bak-24 from #24)"
  say "KV_EVENTS=0, as the grid ran"
}

# The mark routes on counters the engine publishes by default, but a version that
# renamed or dropped them would leave every reading unread, and HitRate.Under
# keeps the match when it cannot read -- so the whole arm would silently be the
# spill-off arm again, under a label saying otherwise. Checked at the moment it
# matters rather than trusted.
require_prefix_cache_counters() {
  local port url
  for port in $(./ops/fleet.sh replicas | tr ',' '\n' | sed 's/.*://'); do
    url="http://127.0.0.1:$port/metrics"
    curl -sf "$url" 2>/dev/null | grep -q "^vllm:prefix_cache_queries_total" \
      || fatal "replica on $port does not publish vllm:prefix_cache_queries_total; the mark would be unread and every match kept"
  done
  say "every replica publishes the prefix-cache counters"
}

# One mark per directory, stamped and checked. A directory holding another mark's
# cells would be resumed as this mark's.
claim_dir() {
  local dir="$1" mark="$2" held
  mkdir -p "$dir"
  if [[ -f "$dir/MARK" ]]; then
    held="$(cat "$dir/MARK")"
    [[ "$held" == "$mark" ]] \
      || fatal "$dir holds cells measured at mark $held; resume does not compare spill, so this run would report them cached and measure nothing at $mark"
  else
    echo "$mark" > "$dir/MARK"
  fi
}

# --- One point ----------------------------------------------------------------
run_point() {
  local mark="$1" ws="$2" skew="$3"
  say "$POLICY residency $mark: WS $ws skew $skew ($REPS reps)"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$RUN/m$mark/ws$ws-skew$skew" \
    -policy "$POLICY" -spill "$mark/0" \
    -concurrency "$CONC" \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$REPS" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GRID_GEOMETRY -working-set "$ws" -skew "$skew" \
    >> "$LOG" 2>&1 || fatal "$POLICY at mark $mark, WS $ws skew $skew failed; see $LOG"
}

# flagged_cells prints point <TAB> id <TAB> reasons for each flagged cell under
# one mark's directory.
flagged_cells() {
  python3 - "$RUN/m$1" <<'PY'
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
  local mark="$1" point="$2" id="$3" stamp
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$RUN/m$mark/$point/discarded"
  for ext in json jsonl; do
    if [[ -f "$RUN/m$mark/$point/cells/$id.$ext" ]]; then
      mv "$RUN/m$mark/$point/cells/$id.$ext" "$RUN/m$mark/$point/discarded/$id-$stamp.$ext"
    fi
  done
}

# --- One mark: the fleet cold, the router armed, all twelve points ------------
run_mark() {
  local mark="$1" started
  started=$(date +%s)
  say "--- residency mark $mark: starting ---"
  claim_dir "$RUN/m$mark" "$mark"

  fleet_cycle
  require_prefix_cache_counters
  ./ops/probe-usage.sh >> "$LOG" 2>&1 \
    || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"

  EVID=$RUN/m$mark/evidence
  mkdir -p "$EVID"
  # -hit-rate-low-water is the only spill flag: the load condition stays off, so
  # the mark is the only thing that can decline a match. The router turns the
  # replica scrape on by itself when a residency threshold is set.
  start_router "$POLICY" "$RUN/m$mark/router-$POLICY.jsonl" \
    -prefix-calibration "$GRID_CAL" -hit-rate-low-water "$mark"

  for ws in "${WORKING_SETS[@]}"; do
    for skew in "${SKEWS[@]}"; do
      run_point "$mark" "$ws" "$skew"
    done
  done
  stop_router
  say "residency mark $mark: all 12 points done in $((($(date +%s) - started) / 60)) min"

  # One re-run pass, not a loop, for the reason run-pressure-rerun.sh gives: a
  # cell that will not settle on a second attempt is a finding about its point.
  mapfile -t FLAGGED < <(flagged_cells "$mark")
  if [[ ${#FLAGGED[@]} -eq 0 ]]; then
    say "mark $mark: no flagged cells"
    return
  fi
  say "mark $mark: ${#FLAGGED[@]} flagged cell(s): setting aside and re-running once"
  local points=() line point id reasons ws skew
  for line in "${FLAGGED[@]}"; do
    IFS=$'\t' read -r point id reasons <<< "$line"
    say "  $point  $id  -- $reasons"
    set_aside "$mark" "$point" "$id"
    [[ " ${points[*]:-} " == *" $point "* ]] || points+=("$point")
  done

  # Cold again, so a re-run point does not inherit the cache state the rest of
  # the mark left behind; its own evidence directory, so the arm's router log is
  # not overwritten.
  fleet_cycle
  ./ops/probe-usage.sh >> "$LOG" 2>&1 \
    || fatal "after the restart the fleet does not report cached prompt tokens; see $LOG"
  EVID=$RUN/m$mark/evidence/rerun
  mkdir -p "$EVID"
  start_router "$POLICY" "$RUN/m$mark/router-$POLICY-rerun.jsonl" \
    -prefix-calibration "$GRID_CAL" -hit-rate-low-water "$mark"
  for point in "${points[@]}"; do
    ws="${point#ws}"; ws="${ws%%-skew*}"
    skew="${point##*-skew}"
    run_point "$mark" "$ws" "$skew"
  done
  stop_router

  mapfile -t STILL < <(flagged_cells "$mark")
  say "mark $mark: still flagged after re-running -- reported, not retried: ${#STILL[@]}"
  local l
  for l in "${STILL[@]}"; do say "  ${l//$'\t'/  }"; done
}

# --- Entry --------------------------------------------------------------------
say "=== residency branch across the grid: ${MARKS[*]} ==="
EVID=$RUN
mkdir -p "$EVID"
fleet_up
require_events_off
require_prefix_cache_counters
./ops/probe-usage.sh >> "$LOG" 2>&1 \
  || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"
say "guards passed: events off, counters published, usage broken down"

if [[ "${1:-}" == "smoke" ]]; then
  # One point, one mark, one short cell, in a directory of its own. What it
  # proves is the part that cannot be checked from here: that the router takes
  # the mark, that bench's label check agrees with it, and that the branch reads
  # a rate rather than finding every replica unread.
  RUN=runs/pressure-residency-smoke
  EVID=$RUN/evidence
  mkdir -p "$EVID"
  CELL=40s; WARM=10s; REPS=1
  start_router "$POLICY" "$RUN/router-smoke.jsonl" \
    -prefix-calibration "$GRID_CAL" -hit-rate-low-water "${MARKS[0]}"
  run_point "${MARKS[0]}" 1 0
  stop_router
  ./ops/fleet.sh down >> "$LOG" 2>&1 || true
  say "=== smoke point done at mark ${MARKS[0]}: fleet down ==="
  say "the cell is in $RUN and is not part of any arm; delete it before the run"
  exit 0
fi

for mark in "${MARKS[@]}"; do
  run_mark "$mark"
done

./ops/fleet.sh down >> "$LOG" 2>&1 || true
say "fleet down: GPUs released"
say "=== residency branch across the grid: complete ==="
