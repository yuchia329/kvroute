#!/usr/bin/env bash
# The load axis, four policies, in one campaign.
#
#   ./run-load-sweep.sh smoke     # one short cell, to prove the pipeline
#   ./run-load-sweep.sh           # all four policies, 7 loads, 3 reps, ~5 h
#   ./run-load-sweep.sh prefix_affinity session_affinity   # a subset, in that order
#
# Why this exists: the 2026-09-08 sweep is the only closed-loop ladder in the
# repository and it predates prefix affinity, so the published goodput-against-load
# figure carries three policies and not the one the project is about. Its cells
# cannot be extended -- a fourth policy measured months later against a fleet that
# was cycled differently is a second measurement, not a fourth column -- so the
# ladder is re-run here for all four policies in one campaign, under one fleet,
# with the frozen workload and SLO every other table in the project uses.
#
# The 2026-09-08 cells are NOT superseded and must not be merged with these: they
# hold a 65 s measured window against this campaign's 100 s, which compare does not
# check and which moves the numbers silently. Compare within this directory only.
#
# Cells are resumable: a cell already on disk is loaded rather than re-run, so an
# interrupted night continues where it stopped. The fleet is left down however this
# ends, because the box is shared.
source ./lib-sweep.sh
set -o pipefail

LOG=load-sweep.log
RUN=runs/load-sweep-4policy
SMOKE=runs/load-sweep-smoke
EVID=$RUN/evidence
mkdir -p "$EVID" "$SMOKE"

# The box is shared and this runs unattended for hours. Every exit path takes the
# fleet down -- a fatal in the middle of the night otherwise leaves five replicas
# holding five cards until somebody notices. Both calls are idempotent, so the
# clean path taking the fleet down before this fires costs nothing.
cleanup() {
  local rc=$?
  [[ -n "${RPID:-}" ]] && kill "$RPID" 2>/dev/null
  say "cleanup: taking the fleet down (exit $rc)"
  ./ops/fleet.sh down >> "$LOG" 2>&1 || true
}
trap cleanup EXIT INT TERM

# The ladder the 2026-09-08 figure drew, so the two are read on the same axis.
# 256 is left off: it is past saturation for every policy there and costs an hour.
LEVELS=1,4,8,16,32,64,128

# Prefix affinity carries the spill rule at the factor the pressure grid settled
# on (#18), because that is the policy every headline in this project reports.
LOAD_IMBALANCE=2
SPILL_LABEL="-spill 0/$LOAD_IMBALANCE"
SPILL_ROUTER="-load-imbalance-factor $LOAD_IMBALANCE"

run_policy() {
  local policy="$1" levels="$2" reps="$3" dir="$4"
  local started=$(date +%s)
  local spill="" router_args=""
  if [[ "$policy" == "prefix_affinity" ]]; then
    spill="$SPILL_LABEL"
    router_args="-prefix-calibration $DERIVED $SPILL_ROUTER"
  fi

  start_router "$policy" "$RUN/router-$policy.jsonl" $router_args
  say "$policy: levels $levels, $reps reps"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$dir" \
    -policy "$policy" $spill \
    -driver closed_loop \
    -concurrency "$levels" \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$reps" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $WORKLOAD \
    >> "$LOG" 2>&1 || { stop_router; fatal "$policy failed; see $LOG"; }
  stop_router

  local elapsed=$(( $(date +%s) - started ))
  say "$policy: ladder done in $((elapsed/3600))h$(( (elapsed%3600)/60 ))m"
}

say "=== load sweep: $(date -u) ==="
fleet_up

if [[ "${1:-}" == "smoke" ]]; then
  CELL=60s; WARM=20s
  run_policy prefix_affinity 4 1 "$SMOKE"
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "=== smoke done, fleet down ==="
  exit 0
fi

# Session affinity and prefix affinity first: they are the comparison the figure
# is missing. If the night is cut short, that pair is the half worth having.
POLICIES=("$@")
[[ ${#POLICIES[@]} -eq 0 ]] && POLICIES=(session_affinity prefix_affinity round_robin least_outstanding)

first=1
for policy in "${POLICIES[@]}"; do
  [[ $first -eq 0 ]] && fleet_cycle
  first=0
  run_policy "$policy" "$LEVELS" "$REPS" "$RUN"
done

./ops/fleet.sh down >> "$LOG" 2>&1
say "=== all policies done, fleet down ==="
