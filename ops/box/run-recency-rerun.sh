#!/usr/bin/env bash
# #29 on the box: the recency axis re-run at a geometry the drift check can read,
# and the WS 3 rung the working-set axis is missing.
#
#   ./run-recency-rerun.sh              # both halves, ~1h40m
#   ./run-recency-rerun.sh recency      # the two open-loop configurations only
#   ./run-recency-rerun.sh ws3          # the closed-loop rung only
#
# ---------------------------------------------------------------------------
# Half one: the recency axis, at a longer cell as well as a longer warm-up
#
# #17 measured this axis and could not publish it: all six cells flagged as
# still warming up, the first measured half 51% to 425% slower than the second
# by TTFT p50 against a 25% threshold. #29 asks for the same point re-run with a
# longer warm-up. The recorded rows say the warm-up is only half the story, so
# the cell length moves too. TTFT p50 per visit period, off runs/recency:
#
#   think 30s (period 120s)   p0 288ms  p1  81ms  p2 61ms
#                             p0 346ms  p1 252ms  p2 62ms
#                             p0 340ms  p1 254ms  p2 65ms
#   think 75s (period 300s)   p0 291ms  p1  60ms
#                             p0 292ms  p1  59ms
#                             p0 296ms  p1 64ms
#
# TTFT is not drifting down to a steady state, it is sawtoothing. The rotation
# makes the turn index the round, so the whole conversation pool advances
# together and rolls over to fresh sessions every four rounds; a round is a
# think time, so the period is 4 x think time and TTFT climbs across each period
# as prompts grow, then drops at the rollover. think 30s flagged because its
# window opened 75s into a 120s period and caught the cold opening period in its
# first half only. think 75s flagged for a different reason: a period is 300s
# and the whole cell was 420s, so the window's halves shared no turn index at
# all -- which no warm-up fixes, because it never decays.
#
# So both cells are sized to open on a visit boundary, after the periods the
# rows show were still settling, and to measure exactly two whole periods -- one
# per half of the drift check, so the check is left comparing the fleet against
# itself. That arithmetic is pinned with no fleet running in
# internal/bench/recencywindow_test.go, which also re-asserts that the longer
# cells still spread the recency axis they exist to plot.
#
# Everything else is #17's point to the value: open-loop at 8 req/s, WS 3, skew
# 1.0, policy 4 with spill off, three repetitions.
#
# ---------------------------------------------------------------------------
# Half two: WS 3
#
# The working-set axis has four points and #17 ran three. It skipped WS 3
# because #16's spill-off reference was the same configuration, but that
# reference is a 300s cell and this axis is a 150s one, so pooling them would
# put two measured windows in one column. This runs the rung at the axis's own
# parameters instead -- closed-loop, 32 users, skew 0, spill off, three
# repetitions, lib-sweep's frozen CELL and WARM -- so the four points are one
# measurement.
#
# ---------------------------------------------------------------------------
# Both halves write into directories of their own. The 2026-09-10 cells were
# recorded before bench put a cell's length and warm-up on its record, so they
# carry neither, and the guard that refuses to resume cells of another geometry
# cannot fire on them: pointed at runs/recency it would silently adopt the
# flagged cells as its own. New directories are the whole defence.
#
# Leaves the fleet down however it ends, because the box is shared.
source ./lib-sweep.sh
set -o pipefail

WHAT="${1:-all}"
case "$WHAT" in all|recency|ws3) ;; *) fatal "usage: $0 [all|recency|ws3]" ;; esac

LOG=recency-rerun.log
RECENCY=runs/recency-rerun
LADDER=runs/divergence
EVID=$RECENCY/evidence
mkdir -p "$EVID" "$LADDER"

RATE=8
KV_CAPACITY=629760
# -kv-capacity is passed on every cell alongside -working-set, so each cell
# records the ratio it actually ran at rather than the one it was labelled with:
# an unstated WS is an absence and divergence prints it as `unstated`.
GEOMETRY="-workload multiturn -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -seed 1 \
 -kv-capacity $KV_CAPACITY"

RPID=""
cleanup() {
  if [[ -n "$RPID" ]]; then kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; fi
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "fleet down; GPUs released"
}
# ARMED BELOW, AFTER THE GATES, AND NOT HERE. The box is shared and one of those
# gates refuses to start when somebody else's bench or router is already up --
# so a trap armed at this point would answer that refusal by taking their fleet
# down on the way out. The trap may only own a fleet this script brought up.

started=$(date -u +%s)
elapsed() { echo "$(( ($(date -u +%s) - started) / 60 ))m"; }
say "=== #29: recency re-run and the WS 3 rung ==="

# ---- gates -----------------------------------------------------------------
# The engine configuration every cell of this measurement shares. #17's cells
# ran before KV cache events existed, and a fleet publishing them is a different
# engine configuration (ADR-0010) -- so a re-run under events would not be
# re-running the same point, and the WS 3 rung would not join the three rungs it
# is meant to complete.
[[ "$(./ops/fleet.sh env KV_EVENTS)" == "0" ]] \
  || fatal "KV_EVENTS is not 0 in ops/versions.env. #17's cells ran without them, and a fleet publishing them is another engine configuration: this re-run would not be the same point, and WS 3 would not join the other three rungs"

[[ "$(./ops/fleet.sh env ENABLE_PROMPT_TOKENS_DETAILS)" == "1" ]] \
  || fatal "ENABLE_PROMPT_TOKENS_DETAILS is not 1 in ops/versions.env, so every divergence column would be null. Set it there -- not in the shell -- and re-run"

# Is anybody else driving the cards?
#
# Checking for a live bench or router is NOT enough, and the way it fails is the
# dangerous one. A sweep spends minutes between its cells cycling the fleet, and
# in that window it has neither: a check that looked only for those two would
# find the box idle, and fleet_up's first act is `fleet.sh down` -- so this
# script would take somebody else's fleet out from under them at the one moment
# they could not be seen. #31's load-denominator run was in exactly that state
# on 2026-09-12 when this gate was written.
#
# So the driver script is what is looked for, being the thing that lives for the
# whole run. `pgrep -af` rather than `-f` so the message can name what it found,
# and this script's own name is filtered out instead of its pid: launched under
# `setsid nohup` there is more than one pid in its own tree, and all of them
# carry the name.
others="$(pgrep -af "[r]un-[a-z0-9-]*\.sh" 2>/dev/null | grep -v "$(basename "$0")" || true)"
if [[ -n "$others" ]]; then
  say "another box driver is running:"
  say "$others"
  fatal "not starting: a sweep between its cells has no bench and no router, and fleet_up would take its fleet down"
fi
# -f and not -x. `pgrep -x` matches /proc/PID/stat's comm, which the kernel
# truncates to 15 characters; "bench-linux-amd64" is 17 and "router-linux-amd64"
# is 18, so an -x pattern for either can never match and the check would pass on
# a fleet somebody is actively driving by hand.
if pgrep -f "[b]ench-linux-amd64" > /dev/null 2>&1 || pgrep -f "[r]outer-linux-amd64" > /dev/null 2>&1; then
  fatal "a bench or router is still up against this fleet; not starting"
fi

# Held for the whole run, so the drivers that do take it queue rather than race.
# Not every driver on the box does -- run-load-denominator.sh does not -- which
# is why the check above is the real gate and this is the belt to its braces.
exec 9> /tmp/kvroute-sweep.lock
flock -n 9 || fatal "another run holds /tmp/kvroute-sweep.lock; not starting"

# The index's TTL is derived from the engines' own idle-before-evict tail, and
# the recency curve is read against it. An empty histogram falls back to a
# chosen 20s and the whole point of plotting against a measured 57s is lost.
[ -f "$DERIVED" ] || fatal "no derived calibration at $DERIVED"
case "$(python3 -c "
import json
h = json.load(open('$DERIVED'))['block_idle_before_evict']
print('OK' if h.get('read') and h.get('count', 0) > 0 else 'EMPTY')")" in
  OK) say "calibration carries a real eviction histogram" ;;
  *) fatal "$DERIVED carries an empty eviction histogram, so the TTL would fall back to 20s chosen" ;;
esac

# The bench has to record the think time, or configuration B resumes
# configuration A's cells and the curve is one think time plotted twice.
./bin/bench-linux-amd64 -h 2>&1 | grep -q "think-time" || fatal "this bench has no -think-time flag"

# Every gate has passed, so from here the fleet is this script's to take down.
trap cleanup EXIT
fleet_up

say "=== verifying the engine reports per-request cached tokens ==="
./ops/probe-usage.sh "http://127.0.0.1:8000" 2>&1 | tee -a "$LOG"
[ "${PIPESTATUS[0]}" -eq 0 ] || fatal "the engine does not report prompt_tokens_details; every divergence column would be null"

# ---- half one: the recency configurations -----------------------------------
# cell and warm are whole visit periods: warm covers the periods the rows show
# were still settling, and the measured window is two periods so each half of
# the drift check gets one full sweep of turn indices.
recency_config() {
  local name="$1" think="$2" cell="$3" warm="$4"
  say "--- $name: think $think, cell $cell, warm-up $warm ($(elapsed)) ---"
  # ADR-0004, and residency itself: a configuration starting on a fleet still
  # holding the last one's blocks would report an index better calibrated than
  # it is, which is exactly the quantity under measurement.
  fleet_cycle
  start_router prefix_affinity "$RECENCY/router-$name.jsonl" -prefix-calibration "$DERIVED"
  ./bin/bench-linux-amd64 -router http://127.0.0.1:8080 -dir "$RECENCY/$name" \
    -policy prefix_affinity -spill 0/0 \
    -driver open_loop -arrival-rates "$RATE" -think-time "$think" \
    -replicas "$SPECS" -model "$MODEL" -gpu-indexes "$GPUS" -slo-from "$SLO" \
    $GEOMETRY -working-set 3 -skew 1.0 \
    -cell-duration "$cell" -warmup "$warm" -settle "$SETTLE" -repetitions "$REPS" \
    >> "$EVID/recency-$name.log" 2>&1 || { stop_router; fatal "$name's bench failed; see $EVID/recency-$name.log"; }
  stop_router
  RPID=""
  # start_router names the router log after the POLICY (lib-sweep.sh), and all
  # three stages here run prefix_affinity -- so without this the next stage
  # truncates this one's log and only the last survives.
  mv -f "$EVID/router-prefix_affinity.log" "$EVID/router-$name.log" 2>/dev/null || true
  # A cell that sent nothing is the failure this layout exists to prevent, and it
  # is silent: the run log looks identical. Counted against what this geometry
  # offers -- cell seconds x rate x repetitions -- rather than a flat floor, because
  # a stage that died after one of three repetitions clears any flat floor and then
  # gets published as though it were whole.
  local rows want
  want=$(( ${cell%s} * RATE * REPS ))
  rows=$(cat "$RECENCY/$name"/cells/*.jsonl 2>/dev/null | wc -l)
  say "$name recorded $rows rows of an offered $want"
  [ "$rows" -ge "$want" ] || fatal "$name recorded $rows rows against the $want its geometry offers -- a repetition is missing, or it resumed another configuration's cells"
}

# ---- half two: the missing rung ---------------------------------------------
# lib-sweep's CELL and WARM, which are the working-set axis's own: this rung has
# to be poolable with the three already measured, and a cell of another length
# is another measurement under the same id.
ws3_rung() {
  say "--- WS 3, skew 0, spill off, closed loop at 32 users ($(elapsed)) ---"
  fleet_cycle
  start_router prefix_affinity "$LADDER/router-ws3.jsonl" -prefix-calibration "$DERIVED"
  ./bin/bench-linux-amd64 -router http://127.0.0.1:8080 -dir "$LADDER/ws3" \
    -policy prefix_affinity -spill 0/0 \
    -replicas "$SPECS" -model "$MODEL" -gpu-indexes "$GPUS" -slo-from "$SLO" \
    $GEOMETRY -working-set 3 -skew 0 \
    -concurrency 32 -cell-duration $CELL -warmup $WARM -settle $SETTLE -repetitions $REPS \
    >> "$EVID/ladder-ws3.log" 2>&1 || { stop_router; fatal "WS 3's bench failed; see $EVID/ladder-ws3.log"; }
  stop_router
  RPID=""
  mv -f "$EVID/router-prefix_affinity.log" "$EVID/router-ws3.log" 2>/dev/null || true
  # Closed loop, so the count is not arithmetic from the geometry; three cells'
  # worth of records is what says every repetition ran.
  local rows cells
  rows=$(cat "$LADDER/ws3"/cells/*.jsonl 2>/dev/null | wc -l)
  cells=$(ls "$LADDER/ws3"/cells/*.json 2>/dev/null | wc -l)
  say "WS 3 recorded $rows rows across $cells cells"
  [ "$cells" -eq "$REPS" ] || fatal "WS 3 recorded $cells cells, not the $REPS its repetitions offer"
  [ "$rows" -gt 1000 ] || fatal "WS 3 recorded only $rows rows"
}

case "$WHAT" in
  all)     recency_config think30 30s 480s 240s
           recency_config think75 75s 900s 300s
           ws3_rung ;;
  recency) recency_config think30 30s 480s 240s
           recency_config think75 75s 900s 300s ;;
  ws3)     ws3_rung ;;
esac

./ops/fleet.sh down >> "$LOG" 2>&1

# ---- read it ----------------------------------------------------------------
# Whether a cell flagged is the question this run exists to answer, so it is
# printed rather than left in the records for somebody to notice.
say "=== cleanliness: the flag this re-run is trying to clear ==="
python3 - "$RECENCY" "$LADDER" <<'PY' 2>&1 | tee -a "$LOG"
import glob, json, os, sys
for base in sys.argv[1:]:
    for cell in sorted(glob.glob(os.path.join(base, "*", "cells", "*.json"))):
        d = json.load(open(cell))
        s = d["summary"]
        flags = s.get("flag_reasons") or []
        print("  %-44s drift %+7.3f  requests %5d  %s" % (
            os.path.relpath(cell, base), s.get("warmup_drift", 0), s.get("requests", 0),
            "CLEAN" if not s.get("flagged") else "FLAGGED: " + "; ".join(flags)))
PY

if [[ "$WHAT" != "ws3" ]]; then
  say "=== recency axis ==="
  ./bin/divergence-linux-amd64 -out "$RECENCY/recency.md" \
    "$RECENCY/think30" "$RECENCY/think75" 2>&1 | tee -a "$LOG"
  say "=== per configuration, so the two can be read as a robustness check ==="
  for c in think30 think75; do
    say "--- $c ---"
    ./bin/divergence-linux-amd64 -out "$RECENCY/recency-$c.md" "$RECENCY/$c" 2>&1 \
      | sed -n '/since the session was last served/,/^$/p;/^| /p' | tail -20
  done
fi

if [[ "$WHAT" != "recency" ]]; then
  say "=== working-set axis, all four points ==="
  ./bin/divergence-linux-amd64 -out "$LADDER/working-set.md" \
    -calibration-out "$LADDER/divergence.json" \
    "$LADDER/ws0.25" "$LADDER/ws1" "$LADDER/ws3" "$LADDER/ws8" 2>&1 | tee -a "$LOG"
fi

say "=== DONE in $(elapsed) ==="
