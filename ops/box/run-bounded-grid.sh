#!/usr/bin/env bash
# #36 on the box: bounded session affinity -- session affinity's ring with a
# load bound -- put on the pressure grid beside the four policies #18 measured.
#
#   ./run-bounded-grid.sh smoke     # one 60 s cell: the bound deflects, the label reaches the record
#   ./run-bounded-grid.sh derisk    # WS 1 / skew 0 and WS 1 / skew 1.4, 3 reps each, ~40 min
#   ./run-bounded-grid.sh grid      # all twelve points, 36 cells, ~3.2 h
#   ./run-bounded-grid.sh followup  # the same-night control, then bound 0.5, at the two WS 1 points, ~2.2 h
#
# Every margin this repo has published for prefix affinity is over session
# affinity, which is blind to load on purpose, and the report's own second
# finding says the win is load rather than cache reuse. Envoy and HAProxy turn a
# load bound on that same hash with one setting. This is that policy, at
# CacheRoute's bound, and the question is how much of the +66% at WS 1 / skew 0
# belongs to the bound rather than to the index (ADR-0016).
#
# derisk comes first because #36 says so: it is the cheapest test of whether the
# headline survives, and if the bound closes more than half of the gap the README
# correction is published before anything else in #35 is.
#
# The two de-risk points go into a directory of their own and the grid runs them
# again. That costs two points of fleet time and buys the one property every
# other policy's grid has: the twelve points run in bench.PressureGrid()'s order
# on one cycled fleet, so each point inherits the cache state the same
# predecessors left it under every policy. De-risk cells run first on a cold
# fleet, which no other policy's WS 1 points did, so they answer the de-risk
# question and are kept as a record beside the grid rather than pooled into it.
#
# The cells are comparable with runs/pressure's -- #18's grid -- and with nothing
# else: the same frozen geometry, the same 300 s cells, KV cache events off
# (ADR-0010), five cards (ADR-0013). They land in runs/pressure-bounded rather
# than in runs/pressure so that a published measurement's directory is never
# written to by a later run; pressuremap and regimemap are handed both.
#
# followup was added after the grid ran (ADR-0016's amendment). smoke, derisk and
# grid ran on 2026-09-19 from this file as committed at dc981b1, md5
# e330a30aca7c, and send what they sent then: the label and the report were made
# general for followup's other policies and nothing else moved. followup answers
# two questions the grid left open, at the two de-risk points and under the
# de-risk arm's conditions -- each point on a freshly cycled fleet -- so its cells
# read against runs/pressure-bounded-derisk's:
#
#   control   session_affinity and prefix_affinity again, by these binaries, on
#             this night. #18's baselines are nine days older than the bounded
#             cells. Into runs/pressure-bounded-control; never pooled into #18's.
#   bound 0.5 the one looser bound. 0.25 deflected 17-19% of turns at skew 0 above
#             WS 1 for no gain, which is what a bound that is too tight looks like.
#             Into runs/pressure-bounded-eps0.5: a cell id does not carry the
#             bound, so two bounds never share a directory, and bench refuses it.
#
# Resumable: cells on disk are loaded rather than re-run. It leaves the fleet
# down however it ends, because the box is shared.
source ./lib-sweep.sh
set -o pipefail

LOG=bounded-grid.log
RUN=runs/pressure-bounded
DERISK=runs/pressure-bounded-derisk
SMOKE=runs/bounded-smoke
CONTROL=runs/pressure-bounded-control
LOOSER=runs/pressure-bounded-eps0.5
PUBLISHED=runs/pressure
EVID=$RUN/evidence
mkdir -p "$EVID" "$DERISK" "$SMOKE" "$CONTROL" "$LOOSER"

# --- The grid: #18's, to the value ---------------------------------------------
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
POLICY=bounded_session_affinity

# --- The bound: stated, never defaulted ----------------------------------------
# bench.CacheRouteInflightBound. A replica keeps a session while its inflight is
# below ceil(1.25 x (fleet inflight + 1) / replicas). No other bound is swept:
# the objection that 0.25 was a bad choice is left open on purpose (#35).
BOUND=0.25
# The one looser bound followup runs, at two points only. Not a sweep.
LOOSER_BOUND=0.5
# What the harness is told the router is running, spelled to both and checked by
# bench before the first cell. followup changes it per arm.
LABEL="-inflight-bound $BOUND"

# Prefix affinity as #18's grid ran it: bench.Chosen's spill point and the
# derived calibration, from run-pressure-grid.sh to the value.
PREFIX_LABEL="-spill 0/2"
PREFIX_ROUTER="-prefix-calibration $DERIVED -load-imbalance-factor 2"

SMOKE_CELL=60s
SMOKE_WARM=20s
# The smoke runs where the bound has something to do. At skew 0 the ring lands
# sessions evenly and a bound that deflected nothing would prove nothing.
SMOKE_WS=1
SMOKE_SKEW=1.4

RPID=""
cleanup() {
  if [[ -n "$RPID" ]]; then kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; fi
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "fleet down; GPUs released"
}

run_point() {
  local base="$1" ws="$2" skew="$3" reps="$4"
  local cell="$CELL" warm="$WARM"
  if [[ "$base" == "$SMOKE" ]]; then cell="$SMOKE_CELL"; warm="$SMOKE_WARM"; fi
  say "$POLICY [$LABEL]: WS $ws skew $skew ($reps reps, $cell cells)"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$base/ws$ws-skew$skew" \
    -policy "$POLICY" $LABEL -fleet-kv-events=false \
    -concurrency "$CONC" \
    -cell-duration $cell -warmup $warm -settle $SETTLE \
    -repetitions "$reps" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GRID_GEOMETRY -working-set "$ws" -skew "$skew" \
    >> "$LOG" 2>&1 || fatal "$POLICY at WS $ws skew $skew failed; see $LOG"
}

# bound_report prints how a point's decisions split between turns the bound let
# stand and turns it moved, beside the goodput. The deflected share is the
# policy's cost, counted: each is a turn sent away from the replica that served
# the conversation.
bound_report() {
  python3 - "$@" <<'PY' | tee -a "$LOG"
import glob, json, sys
for point in sys.argv[1:]:
    for path in sorted(glob.glob(point + "/cells/*.json")):
        c = json.load(open(path))
        s, d = c["summary"], c["summary"]["decisions"]
        total = sum(v for v in d.values() if isinstance(v, int))
        share = (lambda n: 0.0 if total == 0 else 100.0 * n / total)
        print("  %s  bound %s  goodput %.2f/s  p90 TTFT %.0f ms  kept %d (%.1f%%)  deflected %d (%.1f%%)  unidentified %d  undecided %d  flagged %s %s" % (
            c["id"], c["inflight_bound"], s["goodput_rps"], s["ttft_p90_ns"] / 1e6,
            d.get("bounded_session_affinity", 0), share(d.get("bounded_session_affinity", 0)),
            d.get("bound_deflected", 0), share(d.get("bound_deflected", 0)),
            d["session_unidentified"], d["undecided"],
            s["flagged"], s.get("flag_reasons", "")))
PY
}

# --- Entry ---------------------------------------------------------------------
MODE="${1:-derisk}"
case "$MODE" in smoke|derisk|grid|followup) ;; *) fatal "unknown mode $MODE: one of smoke, derisk, grid, followup" ;; esac
say "=== bounded session affinity (#36): $MODE ==="

# ---- gates --------------------------------------------------------------------
# No trap yet: a refusal below must not take somebody else's fleet down on the
# way out. See ops/box/README.md and run-recency-rerun.sh, where these were found.
[[ "$(./ops/fleet.sh env KV_EVENTS)" == "0" ]] \
  || fatal "KV_EVENTS is not 0 in ops/versions.env. #18's grid ran without them, and a fleet publishing them is another engine configuration (ADR-0010): these cells could not be compared with runs/pressure's"
[[ "$(./ops/fleet.sh env ENABLE_PROMPT_TOKENS_DETAILS)" == "1" ]] \
  || fatal "ENABLE_PROMPT_TOKENS_DETAILS is not 1 in ops/versions.env. Set it there -- not in the shell -- and re-run"
[[ "$(./ops/fleet.sh env REPLICA_GPUS)" == "0 1 2 4 5" ]] \
  || fatal "REPLICA_GPUS is not the five cards #18's grid ran on (ADR-0013)"

others="$(pgrep -af "[r]un-[a-z0-9-]*\.sh" 2>/dev/null | grep -v "$(basename "$0")" || true)"
if [[ -n "$others" ]]; then
  say "another box driver is running:"
  say "$others"
  fatal "not starting: a sweep between its cells has no bench and no router, and fleet_up would take its fleet down"
fi
if pgrep -f "[b]ench-linux-amd64" > /dev/null 2>&1 || pgrep -f "[r]outer-linux-amd64" > /dev/null 2>&1; then
  fatal "a bench or router is still up against this fleet; not starting"
fi
# A fleet already up under this user is not assumed to be this script's to take
# down: fleet_up's first act is fleet.sh down. If it is a dead run's leftover,
# take it down by hand and re-run -- that is one command, and guessing wrong
# costs somebody a night.
if pgrep -u "$(id -u)" -f "[v]llm" > /dev/null 2>&1; then
  fatal "a vllm process of yours is already running. If it is a dead run's fleet, ./ops/fleet.sh down and re-run; if it is a live run's, wait for it"
fi
exec 9> /tmp/kvroute-sweep.lock
flock -n 9 || fatal "another run holds /tmp/kvroute-sweep.lock; not starting"

# The binaries have to be the ones that know the policy and its label, or the
# first cell fails twelve minutes into a fleet bring-up instead of here.
./bin/router-linux-amd64 -h 2>&1 | grep -q "inflight-bound" || fatal "this router has no -inflight-bound flag: run make box-sync from a checkout at or after #46"
./bin/bench-linux-amd64 -h 2>&1 | grep -q "inflight-bound" || fatal "this bench has no -inflight-bound flag: run make box-sync from a checkout at or after #46"

if [[ "$MODE" == "followup" ]]; then
  [ -f "$DERIVED" ] || fatal "no derived calibration at $DERIVED, and the control's prefix_affinity cells need the one #18's grid ran with"
fi

# Every gate has passed, so from here the fleet is this script's to take down.
trap cleanup EXIT
started=$(date +%s)
fleet_up
./ops/probe-usage.sh >> "$LOG" 2>&1 \
  || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"
say "usage probe passed: the engines report cached prompt tokens"
cp ops/versions.env "$EVID/versions.env"
md5sum bin/bench-linux-amd64 bin/router-linux-amd64 "$0" lib-sweep.sh > "$EVID/md5sums-$MODE.txt"

case "$MODE" in
  smoke)
    start_router "$POLICY" "$SMOKE/router.jsonl" -inflight-bound "$BOUND"
    run_point "$SMOKE" "$SMOKE_WS" "$SMOKE_SKEW" 1
    stop_router; RPID=""
    bound_report "$SMOKE/ws$SMOKE_WS-skew$SMOKE_SKEW"
    python3 - "$SMOKE/ws$SMOKE_WS-skew$SMOKE_SKEW/cells/$POLICY-c$CONC-r1.json" "$BOUND" <<'PY' | tee -a "$LOG"
import json, sys
c = json.load(open(sys.argv[1]))
d = c["summary"]["decisions"]
ok = (d["bounded_session_affinity"] > 0 and d["bound_deflected"] > 0
      and d["undecided"] == 0 and d["session_unidentified"] == 0
      and c["inflight_bound"] == float(sys.argv[2]))
print("  SMOKE " + ("PASSED" if ok else
      "FAILED: a bound that moved nothing at skew 1.4, a session the router could not identify, a reason this harness does not know, or a cell that does not record its bound"))
sys.exit(0 if ok else 1)
PY
    [[ $? -eq 0 ]] || fatal "smoke failed; see $LOG"
    say "=== smoke passed ==="
    ;;

  derisk)
    # Cycled between the two points as well as before the first. Inside a grid a
    # point inherits its predecessors' cache state and that is fine because it is
    # identical across policies; these two have no such counterpart, so each
    # gets the one state that is reproducible, a cold fleet.
    for skew in 0 1.4; do
      fleet_cycle
      start_router "$POLICY" "$DERISK/router-$POLICY-ws1-skew$skew.jsonl" -inflight-bound "$BOUND"
      run_point "$DERISK" 1 "$skew" "$REPS"
      stop_router; RPID=""
      bound_report "$DERISK/ws1-skew$skew"
    done
    for skew in 0 1.4; do
      for pair in "session_affinity $POLICY" "$POLICY prefix_affinity" "session_affinity prefix_affinity"; do
        set -- $pair
        ./bin/pressuremap-linux-amd64 -out "runs/pressuremap-bounded-derisk-ws1-skew$skew-$1-vs-$2.md" \
          -baseline "$1" -challenger "$2" "$PUBLISHED/ws1-skew$skew" "$DERISK/ws1-skew$skew" >> "$LOG" 2>&1 \
          || say "pressuremap $1 vs $2 at skew $skew exited non-zero (see $LOG): read its validity section first"
      done
    done
    say "=== de-risk points complete in $(( ($(date +%s) - started) / 60 ))m: runs/pressuremap-bounded-derisk-*.md ==="
    ;;

  grid)
    fleet_cycle
    start_router "$POLICY" "$RUN/router-$POLICY.jsonl" -inflight-bound "$BOUND"
    for ws in "${WORKING_SETS[@]}"; do
      for skew in "${SKEWS[@]}"; do
        run_point "$RUN" "$ws" "$skew" "$REPS"
        bound_report "$RUN/ws$ws-skew$skew"
      done
    done
    stop_router; RPID=""
    elapsed=$(( $(date +%s) - started ))
    say "$POLICY: all 12 points done in $((elapsed/3600))h$(( (elapsed%3600)/60 ))m"
    # One map per baseline, never one table with an unnamed reference (ADR-0016).
    ./bin/pressuremap-linux-amd64 -out runs/pressuremap-bounded-vs-prefix.md \
      -baseline "$POLICY" -challenger prefix_affinity "$PUBLISHED"/ws*-skew* "$RUN"/ws*-skew* >> "$LOG" 2>&1 \
      || say "pressuremap (bounded vs prefix) exited non-zero (see $LOG): read the map's validity section first"
    ./bin/pressuremap-linux-amd64 -out runs/pressuremap-session-vs-bounded.md \
      -baseline session_affinity -challenger "$POLICY" "$PUBLISHED"/ws*-skew* "$RUN"/ws*-skew* >> "$LOG" 2>&1 \
      || say "pressuremap (session vs bounded) exited non-zero (see $LOG): read the map's validity section first"
    ./bin/regimemap-linux-amd64 -out runs/regimemap-bounded.md "$PUBLISHED"/ws*-skew* "$RUN"/ws*-skew* >> "$LOG" 2>&1 \
      || say "regimemap exited non-zero (see $LOG)"
    say "=== grid complete: runs/pressuremap-bounded-vs-prefix.md, runs/pressuremap-session-vs-bounded.md, runs/regimemap-bounded.md ==="
    ;;

  followup)
    # Each point on a freshly cycled fleet, as the de-risk arm ran: these cells
    # are read against that arm's, so they get its conditions. The cycle is also
    # what ADR-0004 asks for between policies, which send identical bytes.
    follow_point() {
      local base="$1" skew="$2"; shift 2
      fleet_cycle
      start_router "$POLICY" "$base/router-$POLICY-ws1-skew$skew.jsonl" "$@"
      run_point "$base" 1 "$skew" "$REPS"
      stop_router; RPID=""
      bound_report "$base/ws1-skew$skew"
    }
    for skew in 0 1.4; do
      POLICY=session_affinity; LABEL=""
      follow_point "$CONTROL" "$skew"
      POLICY=prefix_affinity; LABEL="$PREFIX_LABEL"
      follow_point "$CONTROL" "$skew" $PREFIX_ROUTER
    done
    say "=== control complete in $(( ($(date +%s) - started) / 60 ))m ==="
    POLICY=bounded_session_affinity; LABEL="-inflight-bound $LOOSER_BOUND"
    for skew in 0 1.4; do
      follow_point "$LOOSER" "$skew" -inflight-bound "$LOOSER_BOUND"
    done
    # Maps over tonight's cells only: the control's baselines with each bound in
    # turn. The two bounds are never handed to one map, which would pool them as
    # repetitions of one policy.
    for skew in 0 1.4; do
      for arm in "derisk-eps0.25 $DERISK" "eps0.5 $LOOSER"; do
        set -- $arm
        ./bin/pressuremap-linux-amd64 -out "runs/pressuremap-bounded-followup-ws1-skew$skew-$1-vs-prefix.md" \
          -baseline bounded_session_affinity -challenger prefix_affinity "$CONTROL/ws1-skew$skew" "$2/ws1-skew$skew" >> "$LOG" 2>&1 \
          || say "pressuremap ($1, skew $skew) exited non-zero (see $LOG): read its validity section first"
      done
    done
    say "=== followup complete in $(( ($(date +%s) - started) / 60 ))m: $CONTROL, $LOOSER ==="
    ;;
esac
