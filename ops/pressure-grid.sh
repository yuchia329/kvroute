#!/usr/bin/env bash
# Sweep every point of the pressure grid for ONE policy, in one command.
#
#   ops/pressure-grid.sh prefix_affinity
#
# The grid is 4 policies x 4 WS x 3 skew x 3 repetitions = 144 cells. This runs
# the 36 cells of one policy: the twelve points, in the order
# bench.PressureGrid() climbs them, at the frozen cell geometry.
#
# It deliberately does NOT touch the fleet or the router. Two things about a grid
# run are the operator's to get right, and both are irreversible once hours of
# cells are recorded:
#
#   1. THE FLEET IS BROUGHT DOWN AND UP BETWEEN POLICIES. Every point of the grid
#      sends both policies the same bytes — that is what makes them comparable —
#      so the second policy to run would read the first one's blocks out of the
#      replicas' prefix caches. A cold fleet per policy is what stops that.
#      Within a policy no restart is needed: each point has its own slice of the
#      workload's user space (GridWorkloadOffset), so no point re-sends another's
#      conversations.
#
#   1b. THE ROUTER RUNS THE SPILL CONFIGURATION bench.Chosen NAMES. Only prefix
#      affinity has one: start it with -load-imbalance-factor 2 and no honoured
#      low-water mark. bench checks the label against what the router reports
#      before the first cell, so a mismatch fails loudly rather than recording
#      36 cells under the wrong name.
#
#   2. THE POINTS RUN IN THE SAME ORDER FOR EVERY POLICY. The order is fixed
#      here, so simply using this script for all four gives you that. A point
#      leaves the fleet's cache in some state and the next point inherits it;
#      that is realistic and harmless as long as it is identical across
#      policies, and it is a confound the moment it is not.
#
# So per policy: bring the fleet down and up, start the router on that policy,
# run this, stop the router. Then the next policy.
#
# It resumes. Cells already complete are loaded rather than re-run, so an
# interruption costs only the cell in flight, and re-running this after a crash
# is the way to continue.
#
# It runs on the fleet host, where there is no Go toolchain: `make pressure-grid`
# there runs the cross-compiled bin/bench-linux-amd64 rather than building one,
# and `make box-sync` from a checkout is what puts that binary, this script and
# the Makefile on the box (#32).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
cd "$root"

policy="${1:-}"
if [[ -z "$policy" ]]; then
  echo "usage: $0 <policy> [make variables...]" >&2
  echo "  one of round_robin, least_outstanding, session_affinity, prefix_affinity," >&2
  echo "  or exact_residency, which needs the fleet up with KV_EVENTS=1 -- and so does" >&2
  echo "  every policy it is compared against (ADR-0010)" >&2
  echo "" >&2
  echo "  prefix_hash is the sixth, outside idea.md section 5's five (#26). It is the" >&2
  echo "  stateless control for what the prefix index buys, and it runs at the window" >&2
  echo "  and weight PRESSURE_HASH names -- both are stated rather than defaulted:" >&2
  echo "" >&2
  echo "    $0 prefix_hash PRESSURE_HASH=16/4" >&2
  echo "" >&2
  echo "  bounded_session_affinity is the seventh (#36, ADR-0016): session affinity's" >&2
  echo "  ring with a load bound, the second and harder baseline. Its router is started" >&2
  echo "  with INFLIGHT_BOUND at the bound PRESSURE_BOUND labels the cells with, 0.25:" >&2
  echo "" >&2
  echo "    $0 bounded_session_affinity" >&2
  echo "" >&2
  echo "  Anything after the policy is passed to make, which is how the headline" >&2
  echo "  pair gets its extra repetitions and how the spill thresholds are set:" >&2
  echo "" >&2
  echo "    $0 session_affinity REPS=5" >&2
  echo "    $0 prefix_affinity  REPS=5" >&2
  exit 2
fi
# Everything after the policy is a make variable for this sweep.
shift

# The axes, in the order the grid is climbed. They are the values in
# bench.PressureWorkingSets and bench.PressureSkews; a test pins the working set
# axis to the one the characterization sizes session pools against.
working_sets=(0.25 1 3 8)
skews=(0 1 1.4)

# The engine flag belief divergence is measured against. A grid run against a
# fleet without it records a divergence column of nulls and looks exactly like a
# grid that worked, seven hours later — so it is checked before the first cell
# rather than discovered after the last.
if [[ -x ops/probe-usage.sh ]]; then
  echo "== checking the fleet reports per-request cached prompt tokens"
  if ! ops/probe-usage.sh; then
    echo "" >&2
    echo "$0: the fleet does not report per-request cached prompt tokens." >&2
    echo "Belief divergence (#17) would be null for all 144 cells. Restart the fleet with" >&2
    echo "ENABLE_PROMPT_TOKENS_DETAILS=1 in ops/versions.env before running the grid." >&2
    exit 1
  fi
fi

started_at="$(date +%s)"
cell=0
total=$(( ${#working_sets[@]} * ${#skews[@]} ))

for ws in "${working_sets[@]}"; do
  for skew in "${skews[@]}"; do
    cell=$(( cell + 1 ))
    echo ""
    echo "== [$cell/$total] $policy at WS $ws, skew $skew"
    make pressure-grid POLICY="$policy" PRESSURE_WS="$ws" PRESSURE_SKEW="$skew" "$@"
    elapsed=$(( $(date +%s) - started_at ))
    printf '== [%d/%d] done, %dh%02dm elapsed for this policy\n' \
      "$cell" "$total" $(( elapsed / 3600 )) $(( (elapsed % 3600) / 60 ))
  done
done

echo ""
echo "== $policy: all $total points recorded."
echo "When every policy is done, draw the map:"
echo "    make pressure-map"
