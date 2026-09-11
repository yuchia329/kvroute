#!/usr/bin/env bash
# #24 on the box: exact residency against the approximate prefix index across the
# pressure grid, with session affinity as the baseline the share of the gain is
# measured from.
#
#   ./run-exact-grid.sh smoke             # one point of exact residency: events flow, tokens match
#   ./run-exact-grid.sh                   # all three policies, ~9.5 h
#   ./run-exact-grid.sh exact_residency   # named policies only
#
# It runs on a fleet publishing its KV cache events -- KV_EVENTS="1" in
# ops/versions.env -- and refuses to start on one that is not. The events are an
# engine setting (ADR-0010): exact residency cannot run without them, and the other
# two policies are measured again under them so that every cell of a point shares
# one engine configuration. #18's grid in runs/pressure ran without them and is not
# touched: these cells land in runs/pressure-kv-events, and bench refuses to resume
# or compare across the two.
#
# Everything else is #18's grid, on purpose, so that this grid's prefix affinity
# differs from #18's in the events and nothing else: 300 s cells with a 50 s
# warm-up, three repetitions, 32 users, the derived 57 s TTL, and spill at 0/2 --
# which exact residency runs too, being policy 4's own rule.
#
# Resumable, as #18's runner is: cells on disk are loaded rather than re-run. It
# leaves the fleet down however it ends, because the box is shared.
source ./lib-sweep.sh
set -o pipefail

LOG=exact-grid.log
RUN=runs/pressure-kv-events
SMOKE=runs/exact-smoke
EVID=$RUN/evidence
# $SMOKE is named too, not just $RUN's evidence directory: the router writes its
# records before anything else creates the directory it writes them into, and it
# exits rather than creating one, so the smoke would die at the router.
mkdir -p "$EVID" "$SMOKE"

# --- The grid: #18's, to the value --------------------------------------------
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
LOAD_IMBALANCE=2
SPILL_LABEL="-spill 0/$LOAD_IMBALANCE"
GRID_CAL=$DERIVED

RPID=""
cleanup() {
  if [[ -n "$RPID" ]]; then kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; fi
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "fleet down; GPUs released"
}
trap cleanup EXIT

[[ "$(./ops/fleet.sh env KV_EVENTS)" == "1" ]] \
  || fatal "KV_EVENTS is not 1 in ops/versions.env, so the fleet would not publish the events exact residency routes on. Set it there -- not in the shell -- and re-run"

# start_router is lib-sweep.sh's with a longer, polled wait. Exact residency's
# router connects to every engine's event stream and replays what they buffer
# before it listens at all, and three seconds is a guess about how long that takes.
start_router_polled() {
  local policy="$1" records="$2"; shift 2
  ./bin/router-linux-amd64 -listen 127.0.0.1:8080 -replicas "$SPECS" -policy "$policy" \
    -records "$records" "$@" > "$EVID/router-$policy.log" 2>&1 &
  RPID=$!
  local i
  for i in $(seq 1 60); do
    if curl -sf http://127.0.0.1:8080/healthz > /dev/null; then say "$policy: router up"; return 0; fi
    kill -0 "$RPID" 2>/dev/null || break
    sleep 1
  done
  tail -20 "$EVID/router-$policy.log"
  fatal "router did not come up for $policy"
}

# router_args sets ROUTER_ARGS to what a policy's router needs beyond the fleet:
# prefix affinity its calibrated index and the spill point, exact residency every
# engine's event endpoints, the engines' block size and the same spill point.
router_args() {
  case "$1" in
    prefix_affinity)
      ROUTER_ARGS=(-prefix-calibration "$GRID_CAL" -load-imbalance-factor "$LOAD_IMBALANCE") ;;
    exact_residency)
      ROUTER_ARGS=(-kv-events "$(./ops/fleet.sh kv-events)"
                   -kv-events-replay "$(./ops/fleet.sh kv-events-replay)"
                   -kv-block-size "$(./ops/fleet.sh env BLOCK_SIZE)"
                   -load-imbalance-factor "$LOAD_IMBALANCE") ;;
    *)
      ROUTER_ARGS=() ;;
  esac
}

run_point() {
  local base="$1" policy="$2" ws="$3" skew="$4" reps="$5"
  local spill=""
  case "$policy" in prefix_affinity|exact_residency) spill="$SPILL_LABEL" ;; esac

  say "$policy: WS $ws skew $skew ($reps reps)"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$base/ws$ws-skew$skew" \
    -policy "$policy" $spill -fleet-kv-events=true \
    -concurrency "$CONC" \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$reps" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GRID_GEOMETRY -working-set "$ws" -skew "$skew" \
    >> "$LOG" 2>&1 || fatal "$policy at WS $ws skew $skew failed; see $LOG"
}

# residency_report puts exact residency's own account of its streams in the log:
# the evidence ADR-0010 says a run must carry about whether its index was complete.
residency_report() {
  curl -sf http://127.0.0.1:8080/router/stats | python3 -c '
import json, sys
for r in json.load(sys.stdin).get("residency") or []:
    s = r["stream"]
    print("  %-10s blocks %6d  applied %7d  replayed %5d  lost %d  resets %d  reconnects %d  orphaned %d  refused %d  %s" % (
        r["replica"], r["blocks"], s["applied"], s["replayed"], s["lost"], s["resets"],
        max(s["connections"] - 1, 0), r["orphaned"], r["refused"],
        "connected" if s["connected"] else "NOT CONNECTED"))' | tee -a "$LOG"
}

run_policy() {
  local policy="$1" started; started=$(date +%s)
  fleet_cycle
  router_args "$policy"
  start_router_polled "$policy" "$RUN/router-$policy.jsonl" "${ROUTER_ARGS[@]}"
  for ws in "${WORKING_SETS[@]}"; do
    for skew in "${SKEWS[@]}"; do
      run_point "$RUN" "$policy" "$ws" "$skew" "$REPS"
    done
  done
  [[ "$policy" == "exact_residency" ]] && residency_report
  stop_router; RPID=""
  local elapsed=$(( $(date +%s) - started ))
  say "$policy: all 12 points done in $((elapsed/3600))h$(( (elapsed%3600)/60 ))m"
}

# --- Entry ----------------------------------------------------------------------
say "=== exact residency grid (#24) starting: ${*:-all three policies} ==="
fleet_up

./ops/probe-usage.sh >> "$LOG" 2>&1 \
  || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"
say "usage probe passed: the engines report cached prompt tokens"
./ops/probe-tokenize.sh >> "$LOG" 2>&1 \
  || fatal "/tokenize does not give the tokens a chat completion prefills, so exact residency would match nothing; see $LOG"
say "tokenize probe passed: /tokenize gives the chat completion's own prompt tokens"

if [[ "${1:-}" == "smoke" ]]; then
  # One point of exact residency, into a directory of its own so that it can never
  # stand in for a grid cell run in grid order. It proves the three things the
  # night depends on: the router follows every engine, the tokens it asks for match
  # the blocks the engines report, and nothing was lost doing it.
  router_args exact_residency
  start_router_polled exact_residency "$SMOKE/router.jsonl" "${ROUTER_ARGS[@]}"
  run_point "$SMOKE" exact_residency 1 0 1
  residency_report
  stop_router; RPID=""
  python3 - "$SMOKE/ws1-skew0/cells/exact_residency-c32-r1.json" <<'PY' | tee -a "$LOG"
import json, sys
c = json.load(open(sys.argv[1]))
d = c["summary"]["decisions"]
print("  decisions: prefix_affinity %d  cold %d  spill_load %d  untokenized %d  undecided %d" % (
    d["prefix_affinity"], d["cold"], d["spill_load"], d["prompt_untokenized"], d["undecided"]))
print("  residency: lost %d  resets %d  reconnects %d  read %s" % (
    c["residency_lost"], c["residency_resets"], c["residency_reconnects"], c["residency_read"]))
print("  goodput %.2f/s  flagged %s  %s" % (c["summary"]["goodput_rps"], c["summary"]["flagged"], c["summary"].get("flag_reasons", "")))
ok = d["prefix_affinity"] > 0 and d["prompt_untokenized"] == 0 and c["residency_lost"] == 0 and c["residency_read"]
print("  SMOKE " + ("PASSED" if ok else "FAILED: an exact index that took no affinity, a tokenizer that did not answer, or a stream that lost history"))
sys.exit(0 if ok else 1)
PY
  [[ $? -eq 0 ]] || fatal "smoke failed; see $LOG"
  say "=== smoke passed ==="
  exit 0
fi

# The exact-versus-approximate pair first: the headline map needs those two, and
# session affinity only adds the baseline the share of the gain is measured from.
POLICIES=("$@")
[[ ${#POLICIES[@]} -eq 0 ]] && POLICIES=(prefix_affinity exact_residency session_affinity)
for policy in "${POLICIES[@]}"; do
  run_policy "$policy"
done

say "=== exact residency grid complete ==="
./bin/pressuremap-linux-amd64 -out runs/pressuremap-exact.md \
  -baseline prefix_affinity -challenger exact_residency "$RUN"/ws*-skew* >> "$LOG" 2>&1 \
  || say "pressuremap exited non-zero (see $LOG): read the map's validity section before anything else"
say "map: runs/pressuremap-exact.md"
