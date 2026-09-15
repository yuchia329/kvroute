#!/usr/bin/env bash
#
# Bring the whole fleet up and down.
#
#   ops/fleet.sh up        # preflight, then six replicas, one per GPU
#   ops/fleet.sh down
#   ops/fleet.sh status
#   ops/fleet.sh pids      # the replica supervisor pids, for contamination checks
#   ops/fleet.sh replicas  # the -replicas spec for the router and the harness
#   ops/fleet.sh kv-events # the -kv-events spec: each replica's KV event publisher
#   ops/fleet.sh kv-events-replay  # the -kv-events-replay spec: each one's replay socket
#   ops/fleet.sh env MODEL # read one pinned value out of versions.env
#
# Two things this script is careful about:
#
# 1. It refuses to start on dirty GPUs. The box is shared, and a replica that
#    starts beside someone else's job — or beside a leftover of your own, which
#    has already been the actual problem once — measures their workload too,
#    with nothing downstream able to tell afterwards that it did.
#
# 2. It starts replicas one at a time. /home is a network filesystem and the
#    model is 5.4 GB, so six simultaneous loads thrash it and the first cell's
#    timings would include an NFS thundering herd. Each replica is waited to
#    /health before the next starts, plus a settle pause.
#
# Every replica is launched through ops/replica.sh, which is the only thing that
# knows how to build a vLLM command line. This script holds no engine settings
# of its own: a second copy of them is exactly how two cells end up running
# different kernels.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(dirname "$here")"
# shellcheck source=versions.env
source "$here/versions.env"

run_dir="$repo/$RUN_DIR"

die() { echo "fleet: $*" >&2; exit 1; }

# preflight_bin locates the compiled preflight. There is no Go toolchain on the
# box, so it is cross-compiled on the Mac with `make linux` and copied over
# beside the router.
preflight_bin() {
  local candidate
  for candidate in "${PREFLIGHT_BIN:-}" "$repo/bin/preflight-linux-amd64" "$repo/bin/preflight"; do
    [[ -n "$candidate" && -x "$candidate" ]] && { echo "$candidate"; return 0; }
  done
  die "no preflight binary. Build one with 'make linux' on a machine with Go and copy bin/ across.
     The fleet does not start without it: an unchecked GPU is how a cell gets
     silently contaminated."
}

preflight() {
  local bin
  bin="$(preflight_bin)"
  "$bin" -gpu-indexes "$REPLICA_GPUS" -threshold-mib "$GPU_DIRTY_THRESHOLD_MIB" \
    || die "preflight refused. Wait for the cards to clear, or bring down whatever is holding them."
}

# fleet_gpus reads REPLICA_GPUS into an array. The fleet is named card by card
# rather than counted, because it is not the first n cards: GPU 3 throttles
# thermally under simultaneous load and is left out (#25), so a count would name
# 0..4 and keep the very card the fleet exists to exclude.
fleet_gpus() {
  read -r -a _fleet_gpus <<< "$REPLICA_GPUS"
  (( ${#_fleet_gpus[@]} > 0 )) || die "REPLICA_GPUS is empty in ops/versions.env; it names the cards the fleet runs on"
  (( ${#_fleet_gpus[@]} == REPLICA_COUNT )) \
    || die "REPLICA_GPUS lists ${#_fleet_gpus[@]} cards ($REPLICA_GPUS) but REPLICA_COUNT is $REPLICA_COUNT; one of the two is stale"
}

up() {
  preflight
  fleet_gpus

  local i index
  for i in "${!_fleet_gpus[@]}"; do
    index="${_fleet_gpus[$i]}"
    "$here/replica.sh" up "$index"
    if (( i + 1 < ${#_fleet_gpus[@]} )); then
      echo "fleet: settling for ${STARTUP_STAGGER_SECONDS}s before the next replica"
      sleep "$STARTUP_STAGGER_SECONDS"
    fi
  done
  echo "fleet: ${#_fleet_gpus[@]} replicas up on GPUs $REPLICA_GPUS"
  replicas
}

down() {
  # Reverse order, purely so the log reads as the mirror of bring-up.
  fleet_gpus
  local i
  for (( i = ${#_fleet_gpus[@]} - 1; i >= 0; i-- )); do
    "$here/replica.sh" down "${_fleet_gpus[$i]}"
  done
}

# pids prints the supervisor pid of every running replica. The harness reads
# these to tell its own fleet apart from a foreign process on a card: the pid
# here is the `vllm serve` supervisor, and the process actually holding the GPU
# is its EngineCore child, so ownership is resolved by ancestry rather than by
# matching these pids directly.
pids() {
  shopt -s nullglob
  local pid_file target
  for pid_file in "$run_dir"/replica-*.pid; do
    target="$(cat "$pid_file")"
    kill -0 "$target" 2>/dev/null && echo "$target"
  done
}

# replicas prints the -replicas spec the router and the harness take.
# A replica is named for the card it runs on, so replica-4 is GPU 4 on port 8004
# and the ids skip 3 exactly as the fleet does. Renumbering them 0..4 would make
# "replica-3" mean GPU 4, and silently disagree with every measurement already
# recorded against the six-card fleet.
replicas() {
  fleet_gpus
  local index specs=()
  for index in "${_fleet_gpus[@]}"; do
    specs+=("replica-$index=http://127.0.0.1:$(( BASE_PORT + index ))")
  done
  local IFS=,
  echo "${specs[*]}"
}

# endpoints prints a spec naming one ZeroMQ socket per replica, on base + GPU
# index, keyed by the same replica ids as `replicas`: the router pairs each
# replica's events with its HTTP endpoint by id, so the two specs must agree on
# what replica-4 is.
endpoints() {
  fleet_gpus
  local base="$1" index specs=()
  for index in "${_fleet_gpus[@]}"; do
    specs+=("replica-$index=tcp://127.0.0.1:$(( base + index ))")
  done
  local IFS=,
  echo "${specs[*]}"
}

# env prints one value from versions.env, so nothing downstream needs a second
# copy of a setting that is pinned there.
env_value() {
  local name="${1:-}"
  [[ -n "$name" ]] || die "usage: $0 env <NAME>"
  # ${!name+set} rather than [[ -v ]]: the box runs bash 5, but this file is
  # also syntax-checked on a Mac, whose /bin/bash is 3.2.
  [[ -n "${!name+set}" ]] || die "versions.env does not set $name"
  echo "${!name}"
}

case "${1:-}" in
  up)        up ;;
  down)      down ;;
  status)    "$here/replica.sh" status ;;
  pids)      pids ;;
  replicas)  replicas ;;
  kv-events)        endpoints "$KV_EVENTS_BASE_PORT" ;;
  kv-events-replay) endpoints "$KV_EVENTS_REPLAY_BASE_PORT" ;;
  env)       shift; env_value "${1:-}" ;;
  preflight) preflight ;;
  *)         die "usage: $0 up|down|status|pids|replicas|kv-events|kv-events-replay|preflight|env <NAME>" ;;
esac
