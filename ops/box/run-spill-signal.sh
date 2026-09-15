#!/usr/bin/env bash
# The pass that cuts #28's residency grid, and the check that the spill rule's
# two branches read two signals.
#
#   ./run-spill-signal.sh
#
# The spill rule declines a prefix match under two conditions, on the premise
# that they answer two different pressures. Through #16 the first read
# vllm:kv_cache_usage_perc as memory pressure, and that gauge counts the blocks
# held by a replica's *running* batch: over 13,658 rows it tracked the router's
# own inflight at r = 0.973, and two of the three levels swept could not fire at
# the concurrency they ran at.
#
# It then read honoured belief, which was built, measured on 2026-09-12 and
# rejected: the prefix index is calibrated not to over-predict, so it gives up
# beliefs before the engines evict the blocks and is nearly always right about
# whatever it still claims. Its rate came back pinned at 1.0 for 70% of readings,
# and replaying the run through five windows widened the histogram 2.4x without
# improving what the signal predicted.
#
# The branch now reads the engines' own prefix cache hit rate, per replica over a
# moving window. On the same run it had range where the honoured rate had none:
# 68.7% fleet-wide. ADR-0011 has both findings.
#
# Two things have to come off a run before that branch can be swept, and this
# run is both of them at once:
#
#   1. THE CORRELATION. The new signal has to move with inflight materially less
#      than 0.973 did, or it is the same condition in a new unit. Both signals
#      are therefore recorded on the same rows, at the same instants -- which one
#      GET per replica per tick is what guarantees. Reading one off this run and
#      the other off #16's would compare two fleets a month apart.
#
#   2. THE RANGE. A level is only a level if the signal reaches it. #16's grid
#      was cut against a gauge nobody had seen the distribution of, so
#      bench.HitRateLowWaterGrid stayed empty until this pass reported one. It
#      did, on 2026-09-12: min 0.014, p50 0.662, max 0.908 over 3,955 readings,
#      and the grid is 0.55/0.62/0.70. Re-run this pass before trusting those
#      levels against a fleet whose engine version or capacity has changed.
#
# So the spill rule is OFF here. That is not a limitation of the pass, it is the
# pass: the condition has to be disabled for the signal to be observed over its
# natural range rather than over the range its own spilling produces.
#
# WS 3, skew 0, at 32 users -- bench.KVPressureWorkingSet and KVPressureSkew, the
# point the residency axis is measured at. WS 3 offers three times the session
# tokens the fleet can hold, so the replicas evict continuously, which is the
# condition the signal has to be able to see. Skew 0 so that nothing but memory
# pressure moves.
#
# Its own directory, runs/spill-signal. A cell id is policy-load-repetition and
# carries no spill setting, so running this into a directory holding other
# prefix_affinity cells would find them cached and run nothing at all.
#
# It brings the fleet down when it finishes, however it finishes: the box is
# shared.
source ./lib-sweep.sh

LOG=spill-signal.log
RUN=runs/spill-signal
EVID=$RUN/evidence
mkdir -p "$EVID"

# --- The residency axis's own point (bench/spillgrid.go) ---------------------
WS=3
SKEW=0
CONC=32
KV_CAPACITY=629760
GEOMETRY="-workload multiturn -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -seed 1 \
 -kv-capacity $KV_CAPACITY"
POLICY=prefix_affinity

# KV cache events must be OFF. #24 left the box's ops/versions.env at KV_EVENTS=1
# and a backup at /tmp/versions.env.bak-24; every other ticket's cells are
# events-off, and a fleet publishing events is a different engine configuration
# from one that is not (ADR-0007). Checked rather than assumed, because the
# failure is silent: the run would finish and its cells would carry a label that
# does not match anything they are meant to be read beside.
require_events_off() {
  local events
  events="$(./ops/fleet.sh env KV_EVENTS)" || fatal "could not read KV_EVENTS"
  [ "$events" = "0" ] || fatal "ops/versions.env has KV_EVENTS=$events; this pass is events-off (see /tmp/versions.env.bak-24 from #24)"
  say "KV_EVENTS=0, as this pass needs"
}

# The honoured rate is no longer routed on, but it is still recorded beside the
# hit rate, and it is fed from usage.prompt_tokens_details.cached_tokens -- null
# unless the replica was started with --enable-prompt-tokens-details. Gated here
# so the comparison column cannot come back empty, for the reason
# ops/probe-usage.sh exists at all. The hit rate has its own check below: it comes
# off the engine's counters, which need no request-level flag.
require_usage_breakdown() {
  local port
  for port in $(./ops/fleet.sh replicas | tr ',' '\n' | sed 's/.*://'); do
    ./ops/probe-usage.sh "http://127.0.0.1:$port" >> "$LOG" 2>&1 \
      || fatal "replica on $port does not report cached prompt tokens; the residency signal would be empty for the whole run (see $LOG)"
  done
  say "every replica reports per-request cached prompt tokens"
}

# The hit rate comes off two counters the engine publishes by default, but a
# version that renamed or dropped them would leave every reading unread and the
# condition disabled for the run -- the same silent failure in a different place.
# vllmmetrics.Required is asserted against a live replica by the contract test;
# this is the same assertion at the moment it matters.
require_prefix_cache_counters() {
  local port url
  for port in $(./ops/fleet.sh replicas | tr ',' '\n' | sed 's/.*://'); do
    url="http://127.0.0.1:$port/metrics"
    curl -sf "$url" 2>/dev/null | grep -q "^vllm:prefix_cache_queries_total" \
      || fatal "replica on $port does not publish vllm:prefix_cache_queries_total; the hit rate would be unread for the whole run"
  done
  say "every replica publishes the prefix-cache counters"
}

# bench resumes: cells already on disk are loaded rather than re-run, which is
# right for a sweep that died halfway and wrong for this pass. The cell ids here
# are policy-load-repetition and carry no signal in them, so a second observing
# run of a DIFFERENT residency signal lands on the first one's cells, reports
# them cached, runs nothing, and writes a report describing the signal it
# replaced. That happened between the honoured-rate pass and the hit-rate one and
# was caught by hand; this is the check that catches it next time.
require_empty_run_dir() {
  local cells
  cells=$(ls "$RUN"/cells/*.json 2>/dev/null | wc -l | tr -d ' ')
  [ "$cells" = "0" ] \
    || fatal "$RUN already holds $cells cells; bench would resume them and this pass would measure nothing. Move them aside (they are a previous signal's evidence) and re-run"
}

main() {
  say "spill signal: WS $WS skew $SKEW at $CONC users, $REPS reps, spill OFF"
  require_empty_run_dir
  require_events_off
  fleet_up
  require_usage_breakdown
  require_prefix_cache_counters

  # No spill flags: both conditions off, which is what makes this an observing
  # pass. -scrape-replicas is what still puts all three signals on every row with
  # the rule disabled -- the router turns the scrape on by itself only when a
  # residency threshold is set, and here there deliberately is none.
  start_router "$POLICY" "$EVID/router-$POLICY.jsonl" \
    -prefix-calibration "$DERIVED" \
    -scrape-replicas

  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$RUN" \
    -policy "$POLICY" \
    -concurrency "$CONC" \
    -cell-duration $CELL -warmup $WARM -settle $SETTLE \
    -repetitions "$REPS" \
    -model "$MODEL" \
    -gpu-indexes "$GPUS" \
    -replicas "$SPECS" \
    -slo-from "$SLO" \
    $GEOMETRY -working-set "$WS" -skew "$SKEW" \
    >> "$LOG" 2>&1 || { stop_router; fatal "the observing cell failed; see $LOG"; }

  stop_router

  # The report both criteria are read off. It prints each signal's observed range
  # and its correlation with the inflight on the same row, side by side.
  ./bin/spillsignal-linux-amd64 -out "$RUN/spill-signal.md" "$EVID/router-$POLICY.jsonl" \
    >> "$LOG" 2>&1 || fatal "spillsignal could not read the rows; see $LOG"
  say "wrote $RUN/spill-signal.md"
  cat "$RUN/spill-signal.md"
}

trap './ops/fleet.sh down >> "$LOG" 2>&1 || true' EXIT
main
say "done"
