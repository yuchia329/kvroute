#!/usr/bin/env bash
# Run the chaos test for ONE policy: take one replica away under steady open-loop
# load, bring it back, and record the recovery curve (#19, idea.md §7).
#
#   ops/chaos.sh session_affinity kill
#   ops/chaos.sh prefix_affinity  kill
#   make recovery CHAOS_FAULT=kill        # the two curves side by side
#
# Like ops/pressure-grid.sh it deliberately does NOT touch the fleet or the
# router, because the two things a chaos comparison rests on are the operator's
# to get right, and neither can be checked from inside a run:
#
#   1. THE FLEET IS BROUGHT DOWN AND UP BETWEEN POLICIES. Both policies' runs send
#      the same bytes — that is what makes their curves comparable — so the second
#      would otherwise find the first one's conversations in the replicas' caches,
#      and the replica the first run killed would be the only cold one. A fresh
#      fleet per policy gives both runs the same starting point.
#
#   2. THE ROUTER RUNS THE POLICY NAMED HERE. The run checks the name against the
#      router before it offers any load, and refuses a mismatch rather than
#      recording one policy's recovery under another's name. Prefix affinity runs
#      at the spill point bench.Chosen names (-load-imbalance-factor 2).
#
# Kill and drain are separate runs into separate directories. A kill forces the
# reroute and produces the drops nothing can save; a drain should cost nothing
# at all. Run as one they would hide each other's numbers.
#
# It kills and restarts a replica of the fleet through ops/replica.sh, so it runs
# on the fleet host, beside the router. The box has no Go toolchain, so `make
# chaos` there runs the cross-compiled bin/chaos-linux-amd64 rather than building
# one; `make box-sync` from a checkout is what puts that binary, this script and
# the Makefile on it (#32).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
cd "$root"

usage() {
  echo "usage: $0 <policy> <kill|drain> [make variables...]" >&2
  echo "  policy is the one the router is running: session_affinity or prefix_affinity" >&2
  echo "  for the comparison, though any policy can be run." >&2
  echo "" >&2
  echo "  Anything after the fault is passed to make:" >&2
  echo "" >&2
  echo "    $0 session_affinity kill CHAOS_RATE=8 CHAOS_GPU=2" >&2
}

policy="${1:-}"
fault="${2:-}"
if [[ -z "$policy" ]]; then
  usage
  exit 2
fi
case "$fault" in
  kill|drain) ;;
  *) usage; exit 2 ;;
esac
shift 2

make chaos POLICY="$policy" CHAOS_FAULT="$fault" "$@"

echo ""
echo "== $policy: $fault recorded. Bring the fleet down and up, start the router on the"
echo "   other policy, and run this again. When both have run:"
echo "    make recovery CHAOS_FAULT=$fault"
