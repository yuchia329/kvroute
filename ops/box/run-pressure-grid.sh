#!/usr/bin/env bash
# The pressure grid (#18): working set x skew, at one concurrency, per policy.
#
#   ./run-pressure-grid.sh                 # every policy, headline pair first
#   ./run-pressure-grid.sh smoke           # one point, to prove the pipeline
#   ./run-pressure-grid.sh session_affinity prefix_affinity
#
# Resumable. Cells already on disk are loaded rather than re-run, so a run that
# dies costs the cell in flight and nothing before it.
#
# Two invariants this script exists to hold, both of which silently invalidate a
# grid if an operator has to remember them:
#
#   1. The fleet is cycled between policies. Every point sends both policies the
#      same bytes -- that is what makes them comparable -- so the second policy
#      would otherwise read the first one's blocks out of the replicas' prefix
#      caches (ADR-0004). Within a policy no cycle is needed: each point has its
#      own slice of the workload's user space (GridWorkloadOffset, c43348e).
#   2. The points run in the same order for every policy, so that whatever cache
#      state a point inherits from the one before it is identical across
#      policies rather than a confound.
source ./lib-sweep.sh

LOG=pressure-grid.log
RUN=runs/pressure
EVID=$RUN/evidence
mkdir -p "$EVID"

# --- The grid ---------------------------------------------------------------
# The axes, in the order bench.PressureGrid() climbs them.
WORKING_SETS=(0.25 1 3 8)
SKEWS=(0 1 1.4)

# One rung. Spill only fires under pressure, and the imbalance factor was chosen
# on the tunable sweep at this rung -- running the grid elsewhere would apply a
# threshold at a load it was not chosen at.
CONC=32

# The cell is 300s, not lib-sweep.sh's frozen 150s, and this is the one frozen
# value the grid deliberately departs from. It is legitimate because the grid is
# its own table: every point offers its own workload, so no grid cell ever shares
# a table with a comparison cell, and within a point all cells share this length.
#
# The reason is measured rather than assumed. A 150s cell on this fleet produces
# ~2,003 requests, which is ~501 session visits, and a cell cannot touch more
# distinct conversations than it makes visits. The smoke point confirmed it: at
# WS 1 skew 0 it touched 251 of a 307 pool, a realised 0.82 against the model's
# 0.80. Carried across the axis at skew 0 that gives
#
#   150s: WS 0.25/1/3/8 realise 0.25 / 0.82 / 1.26 / 1.48
#   300s:                       0.25 / 0.96 / 1.99 / 2.68
#
# At 150s the top two points are 17% apart -- the axis the grid exists to sweep
# would be three levels wearing four labels. At 300s they are 35% apart, and the
# span goes from 5.9x to 10.7x. The longer cell also doubles the measured window
# from 100s to 250s, which narrows the per-cell noise that the headline delta has
# to clear.
#
# Repetitions stay at 3 rather than rising for the headline pair, because
# repetitions are additive and cell length is not: a rep is its own cell, so if
# the map comes back unresolved, reps 4 and 5 can be added for session and prefix
# affinity without re-running anything. Choosing the cell length wrongly cannot be
# repaired that way.
CELL=300s
WARM=50s
REPS=3

# Measured aggregate fleet KV, five cards. This is the denominator every WS point
# is a ratio of, and without it the cells state no working set at all and the map
# has no axis to draw (GridPoint.Stated).
KV_CAPACITY=629760

# Everything but WS and skew is the frozen workload's geometry, so a grid cell
# differs from a comparison cell in pressure and in nothing else.
GRID_GEOMETRY="-workload multiturn -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -seed 1 \
 -kv-capacity $KV_CAPACITY"

# The spill configuration policy 4 runs under, from bench.Chosen (157d770).
#
# The KV half is 0 -- the condition is OFF, and that is a finding rather than an
# omission. vllm:kv_cache_usage_perc counts blocks held by running requests, so
# it reads the active batch and not cache residency: #16 measured
# kv = 0.02128 + 0.02135 x inflight at r = 0.973. On this fleet the KV branch IS
# the load branch, so any non-zero mark either cannot fire or fires on load. It
# also means this grid cannot answer #18's separability criterion; that waits on
# #28, and the map reports it blocked rather than printing zeros as a result.
LOAD_IMBALANCE=2
SPILL_LABEL="-spill 0/$LOAD_IMBALANCE"
SPILL_ROUTER="-load-imbalance-factor $LOAD_IMBALANCE"

# The derived TTL (57s off the fleet's own eviction tail, 5,076 observations),
# not the 20s fallback that a fleet with an empty histogram falls back to. The
# node cap stays fleet-sized: #17's divergence-scaled cap is a second
# configuration reported against this one, and at 99.7% honoured it moves the cap
# by 0.4% anyway.
GRID_CAL=$DERIVED

# --- One point --------------------------------------------------------------
run_point() {
  local policy="$1" ws="$2" skew="$3" reps="$4"
  local dir="$RUN/ws$ws-skew$skew"
  local spill=""
  [[ "$policy" == "prefix_affinity" ]] && spill="$SPILL_LABEL"

  say "$policy: WS $ws skew $skew ($reps reps)"
  ./bin/bench-linux-amd64 \
    -router http://127.0.0.1:8080 \
    -dir "$dir" \
    -policy "$policy" $spill \
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

# --- One policy: the fleet cycled, the router started, all twelve points -----
run_policy() {
  local policy="$1"
  local reps="$REPS"
  local router_args=""
  local started; started=$(date +%s)

  fleet_cycle

  if [[ "$policy" == "prefix_affinity" ]]; then
    router_args="-prefix-calibration $GRID_CAL $SPILL_ROUTER"
  fi
  start_router "$policy" "$RUN/router-$policy.jsonl" $router_args

  for ws in "${WORKING_SETS[@]}"; do
    for skew in "${SKEWS[@]}"; do
      run_point "$policy" "$ws" "$skew" "$reps"
    done
  done

  stop_router
  local elapsed=$(( $(date +%s) - started ))
  say "$policy: all 12 points done in $((elapsed/3600))h$(( (elapsed%3600)/60 ))m"
}

# --- Entry ------------------------------------------------------------------
say "=== pressure grid starting: $* ==="
fleet_up

# The engine flag belief divergence is measured against. A grid run without it
# records a divergence column of nulls and looks exactly like one that worked,
# hours later.
./ops/probe-usage.sh >> "$LOG" 2>&1 \
  || fatal "the fleet does not report per-request cached prompt tokens; see $LOG"
say "usage probe passed: the engines report cached prompt tokens"

if [[ "${1:-}" == "smoke" ]]; then
  # One point, one policy, to prove the pipeline before committing the night.
  start_router session_affinity "$RUN/router-smoke.jsonl"
  run_point session_affinity 1 0 1
  stop_router
  say "=== smoke point done ==="
  exit 0
fi

# Headline pair first: the map's delta needs those two, and the other two only
# add context columns. If the night is cut short, this is the half worth having.
POLICIES=("$@")
[[ ${#POLICIES[@]} -eq 0 ]] && POLICIES=(session_affinity prefix_affinity round_robin least_outstanding)

for policy in "${POLICIES[@]}"; do
  run_policy "$policy"
done

say "=== pressure grid complete ==="
say "draw the map: ./bin/pressuremap-linux-amd64 -out runs/pressuremap.md $RUN/ws*-skew*"
