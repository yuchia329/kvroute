#!/usr/bin/env bash
# #31: what the spill rule's load condition is comparing against at the rung
# every open-loop measurement of policy 4 is made at, and what the two candidate
# denominators are worth there.
#
#   ./run-load-denominator.sh observe          # the pass that cuts the grid (~10 min)
#   ./run-load-denominator.sh point 0/2        # the settled point, at this rung
#   ./run-load-denominator.sh point 0/2/mean   # the same factor against the fleet mean
#
# The condition declines the best prefix match when its replica is carrying more
# than a factor times the fleet's minimum inflight. #16 swept that factor at 32
# virtual users and #18 settled it at 2, and both are sound at that rung. This
# one is different in kind: the open-loop driver fires on a schedule whether or
# not earlier requests have finished, so the fleet empties between arrivals. At 6
# requests per second over five replicas it holds six requests at the median and
# the quietest replica holds nothing or one — measured 2026-09-12, the minimum was
# never above 1 across 2,361 decisions — which makes "twice the minimum" mean
# "more than two requests": an absolute inflight threshold wearing a ratio's
# clothes.
#
# #19's chaos runs measured what that cost: the rule declined 19-20% of later
# turns against 0.674% at c32, those turns found 11.9% of their prompt cached
# against 73.9% for the turns that stayed, 42% of them missed the SLO, and prefix
# affinity's own baseline fell to 5.33/s against the 5.98/s it holds with the
# rule off. That arm measured the rule rather than the fault and had to be re-run.
#
# THE ORDER MATTERS. Observe first, cut the grid, then sweep:
#
#   1. OBSERVE. One cell, prefix affinity, the rule OFF, open-loop at 6 req/s.
#      Every row carries what the comparison would have been made against, so
#      `make load-comparison` reports how often the ratio degenerated and which
#      candidate points could fire at this rung at all. The rule is off for the
#      reason it is off in the residency pass: a condition that is declining
#      matches is changing the fleet whose load is being observed.
#
#   2. CUT. Write the points that reach into bench.LoadDenominatorGrid, naming
#      this run. A point nobody has seen the range for is not a grid point.
#
#   3. POINT. One invocation per point, into the same directory, read against the
#      spill-off cell the observing pass left there.
#
# Every point gets its own directory under runs/load-denominator, and that is
# load-bearing rather than tidy. A cell id is policy-load-repetition and carries
# no spill point, so all of these cells are prefix_affinity-a6-r0 to r2 whatever
# they were run at. A sweep resumes from the cells already in its directory, so
# pointing the second point at the first one's directory would find every cell
# cached, run nothing, and report the first point's numbers under the second
# point's label. The same reason keeps the whole run out of runs/goodput, which
# already holds prefix_affinity cells at this rate.
#
# It brings the fleet down when it finishes, however it finishes: the box is
# shared.
source ./lib-sweep.sh
set -uo pipefail

LOG=load-denominator.log
RUN=runs/load-denominator
EVID=$RUN/evidence
mkdir -p "$EVID"

# The rung, from bench.LoadDenominatorRate. 6 req/s is where #19 drove policy 4
# and where #31's rows came from, and it is a rung of the goodput ladder so these
# cells sit beside ones already measured there.
RATE=6
POLICY=prefix_affinity

# The frozen workload and SLO the comparison is measured under (lib-sweep.sh).
# Not the tunables sweep's WS 3 or skew 1.4 points: those exist to make one
# pressure live, and this axis is about the rung rather than about pressure. A
# cell here differs from a goodput cell in its spill point and in nothing else.

# KV cache events must be OFF, as they are for every cell this run is read beside.
# Checked rather than assumed, because the failure is silent: the run would
# finish and its cells would carry a label that does not match anything they are
# meant to be compared with. #26's hash grid leaves the box at KV_EVENTS=1, so
# this is a live possibility rather than a formality.
require_events_off() {
  local events
  events="$(./ops/fleet.sh env KV_EVENTS)" || fatal "could not read KV_EVENTS"
  [ "$events" = "0" ] || fatal "ops/versions.env has KV_EVENTS=$events; these cells are events-off"
  say "KV_EVENTS=0, as this pass needs"
}

# One cell at one rung, at the given spill point. An empty point is the
# observing pass: no flags on the router, no label on the cells, which is prefix
# affinity as #15 measured it.
run_cell() {
  local spec="${1:-}" name=off spill_args=() spill_label=()
  if [[ -n "$spec" ]]; then
    # "honoured/load" or "honoured/load/denominator". Only the load half is swept
    # here; the residency condition stays off until #28's grid is cut, so the
    # honoured half of every spec on this axis is 0.
    local load="${spec#*/}"
    if [[ "$load" == */mean ]]; then
      spill_args=(-load-imbalance-factor "${load%/mean}" -mean-inflight-denominator)
    else
      spill_args=(-load-imbalance-factor "${load%/min}")
    fi
    spill_label=(-spill "$spec")
    name="${spec//\//-}"
  fi

  # Every point gets its own cell directory: see the note at the top.
  local dir="$RUN/$name"
  mkdir -p "$dir"

  fleet_up
  require_events_off
  # The +-expansion, not a bare "${array[@]}": under `set -u` an empty array is
  # an unbound variable on bash 3.2, and the observing pass's arrays are empty by
  # design. The box runs a newer bash, but this script is also read on a Mac.
  start_router "$POLICY" "$EVID/router-$name.jsonl" \
    -prefix-calibration "$DERIVED" ${spill_args[@]+"${spill_args[@]}"}

  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$dir" \
    -policy "$POLICY" \
    -driver open_loop -arrival-rates "$RATE" \
    ${spill_label[@]+"${spill_label[@]}"} \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$REPS" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $WORKLOAD \
    >> "$LOG" 2>&1 || { stop_router; fatal "the cell at spill '${spec:-off}' failed; see $LOG"; }

  stop_router
}

trap './ops/fleet.sh down >> "$LOG" 2>&1 || true' EXIT

case "${1:-observe}" in
  observe)
    say "load denominator: observing pass at $RATE req/s, spill OFF, $REPS reps"
    run_cell ""
    # The report both criteria are read off: how often the comparison was a
    # ratio, and which candidate points can fire at this rung.
    ./bin/loadcomparison-linux-amd64 -out "$RUN/load-comparison.md" "$EVID/router-off.jsonl" \
      >> "$LOG" 2>&1 || fatal "loadcomparison could not read the rows; see $LOG"
    say "wrote $RUN/load-comparison.md"
    cat "$RUN/load-comparison.md"
    ;;
  point)
    [[ -n "${2:-}" ]] || fatal "point needs a spill spec, e.g. 0/2 or 0/2/mean"
    [[ -f "$RUN/load-comparison.md" ]] || fatal "no observing pass in $RUN: run ./run-load-denominator.sh observe first, and cut bench.LoadDenominatorGrid against it"
    say "load denominator: ${2} at $RATE req/s, $REPS reps"
    run_cell "$2"
    ;;
  *)
    fatal "unknown mode ${1}: one of observe, point"
    ;;
esac
say "done"
