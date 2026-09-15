#!/usr/bin/env bash
# #19 criterion 8 on the box: take replica-2 away under steady open-loop load and
# record the recovery, for session affinity and then prefix affinity, each on a
# fresh fleet (ADR-0004); then one drain, on the prefix affinity fleet, for the
# zero-drop claim on real replicas. It stands in for ops/chaos.sh and make chaos,
# which build with Go first and so cannot run here.
#
#   ./run-chaos.sh      # ~25 min; leaves the fleet down
#
# Settings: the Makefile's chaos target at CHAOS_RATE=6, with the frozen workload
# and SLO the definitive comparison was measured under (lib-sweep.sh), and prefix
# affinity as the pressure grid ran it (derived 57 s TTL, spill at bench.Chosen).
# Rate 6, not 8: at 8 session affinity already misses the SLO on ~5% of requests
# with all five replicas up (runs/definitive), and after the kill four carry it.
source "$HOME/kvroute/lib-sweep.sh"   # cd ~/kvroute; say, fatal, fleet_up, fleet_cycle, start_router, stop_router
set -uo pipefail

RATE=6
VICTIM=2
TIMING="-duration 300s -warmup 50s -fault-at 100s -recover-at 160s -bucket 5s"
PREFIX_ARGS="-prefix-calibration $DERIVED -load-imbalance-factor 2"
OUT=runs/chaos
EVID=$OUT/evidence
LOG=$OUT/chaos.log
mkdir -p "$EVID"
RPID=""

# However it ends, leave the box as it was found: no router, no replicas.
cleanup() {
  if [[ -n "$RPID" ]]; then kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; fi
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "fleet down"
}
trap cleanup EXIT

run_chaos() {
  local policy="$1" fault="$2" stop="$3"
  say "$policy: $fault run into $OUT/$fault-$policy"
  ./bin/chaos-linux-amd64 -router http://127.0.0.1:8080 -dir "$OUT/$fault-$policy" \
    -policy "$policy" -replica "replica-$VICTIM" -fault "$fault" \
    -stop-cmd "$stop" -start-cmd "ops/replica.sh up $VICTIM" \
    -arrival-rate "$RATE" $TIMING -model "$MODEL" -gpu-indexes "$GPUS" \
    $WORKLOAD -slo-from "$SLO" >> "$LOG" 2>&1 \
    || fatal "$policy $fault run failed; see $LOG"
  say "$policy: $fault run recorded"
}

say "=== chaos (#19) starting: rate $RATE, replica-$VICTIM ==="
fleet_up
start_router session_affinity "$EVID/router-session_affinity.jsonl"
run_chaos session_affinity kill "ops/replica.sh kill $VICTIM"
stop_router; RPID=""

fleet_cycle
start_router prefix_affinity "$EVID/router-prefix_affinity.jsonl" $PREFIX_ARGS
run_chaos prefix_affinity kill "ops/replica.sh kill $VICTIM"
# The drain needs no fresh fleet: nothing compares it, it only has to drop
# nothing. The kill run restarted replica-2 at 160 s and ran on to 300 s, so it
# is back in rotation by now; the run refuses to start if it is not.
run_chaos prefix_affinity drain "ops/replica.sh down $VICTIM"
stop_router; RPID=""
./ops/fleet.sh down >> "$LOG" 2>&1
say "fleet down; GPUs released"

./bin/recovery-linux-amd64 -out runs/recovery-kill.md "$OUT/kill-session_affinity" "$OUT/kill-prefix_affinity" >> "$LOG" 2>&1 \
  || say "the recovery comparison failed; see $LOG"
say "=== ALL DONE ==="
