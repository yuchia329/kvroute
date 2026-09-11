#!/usr/bin/env bash
# #19 criterion 8, the prefix affinity arm again, with the spill rule OFF.
#
# The first attempt ran policy 4 at bench.Chosen (load imbalance factor 2). At
# 6 req/s open-loop that rule fired on 19% of later turns — inflight counts are
# small integers at this load, so the ratio it tests is met constantly — and 42%
# of the turns it moved missed the SLO, as often before the kill as after. The
# curve measured the rule, not the fault. Spill off is how runs/definitive
# measured prefix affinity at this same load, where it held 5.98/s. The spill-on
# run is kept beside this one as evidence of what the rule costs here.
#
#   ./run-chaos-spilloff.sh      # ~11 min; leaves the fleet down
source "$HOME/kvroute/lib-sweep.sh"   # cd ~/kvroute; say, fatal, fleet_up, start_router, stop_router
set -uo pipefail

RATE=6
VICTIM=2
TIMING="-duration 300s -warmup 50s -fault-at 100s -recover-at 160s -bucket 5s"
OUT=runs/chaos
EVID=$OUT/evidence
LOG=$OUT/chaos-spilloff.log
mkdir -p "$EVID"
RPID=""

# However it ends, leave the box as it was found: no router, no replicas.
cleanup() {
  if [[ -n "$RPID" ]]; then kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; fi
  ./ops/fleet.sh down >> "$LOG" 2>&1
  say "fleet down"
}
trap cleanup EXIT

say "=== chaos (#19): prefix affinity, spill off, rate $RATE, replica-$VICTIM ==="
fleet_up
start_router prefix_affinity "$EVID/router-prefix_affinity-spilloff.jsonl" -prefix-calibration "$DERIVED"

./bin/chaos-linux-amd64 -router http://127.0.0.1:8080 -dir "$OUT/kill-prefix_affinity-spilloff" \
  -policy prefix_affinity -replica "replica-$VICTIM" -fault kill \
  -stop-cmd "ops/replica.sh kill $VICTIM" -start-cmd "ops/replica.sh up $VICTIM" \
  -arrival-rate "$RATE" $TIMING -model "$MODEL" -gpu-indexes "$GPUS" \
  $WORKLOAD -slo-from "$SLO" >> "$LOG" 2>&1 \
  || fatal "the spill-off run failed; see $LOG"
say "spill-off run recorded"

stop_router; RPID=""
./ops/fleet.sh down >> "$LOG" 2>&1
say "fleet down; GPUs released"

./bin/recovery-linux-amd64 -out runs/recovery-kill-spilloff.md \
  "$OUT/kill-session_affinity" "$OUT/kill-prefix_affinity-spilloff" >> "$LOG" 2>&1 \
  || say "the comparison failed; see $LOG"
say "=== ALL DONE ==="
