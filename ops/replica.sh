#!/usr/bin/env bash
#
# Supervise one vLLM replica as a bare process.
#
# The box has no container runtime and no permission to install one, which suits
# this project anyway: the chaos test kills a specific process, and killing a
# bare PID is lower-latency and less noisy than killing a container.
#
#   ops/replica.sh up 0       # start replica-0 on GPU 0, port 8000
#   ops/replica.sh down 0     # stop it, letting the engine exit cleanly
#   ops/replica.sh kill 0     # SIGKILL it and its engine at once, as a crash would
#   ops/replica.sh status
#
# Startup asserts the pinned engine version and that the log confirms the forced
# quantization backend, so a drift in either fails here rather than surfacing as
# an unexplained shift in a benchmark cell.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(dirname "$here")"
# shellcheck source=versions.env
source "$here/versions.env"

run_dir="$repo/$RUN_DIR"

die() { echo "replica: $*" >&2; exit 1; }

replica_id()  { echo "replica-$1"; }
replica_port() { echo $(( BASE_PORT + $1 )); }
pid_file()    { echo "$run_dir/$(replica_id "$1").pid"; }
log_file()    { echo "$run_dir/$(replica_id "$1").log"; }

assert_index() {
  [[ "${1:-}" =~ ^[0-9]+$ ]] || die "usage: $0 up|down <gpu index>"
}

# assert_engine_version refuses to start on anything but the pinned version.
assert_engine_version() {
  [[ -x "$VENV/bin/python" ]] || die "no venv at $VENV. Create it with: uv venv $VENV"
  local installed
  installed="$("$VENV/bin/python" -c 'import vllm; print(vllm.__version__)' 2>/dev/null)" \
    || die "vLLM is not importable in $VENV"
  [[ "$installed" == "$VLLM_VERSION" ]] \
    || die "venv has vLLM $installed, pinned version is $VLLM_VERSION. A bump invalidates completed cells."
}

# gpu_cpulist prints the hardware threads local to a GPU, as the kernel reports
# them for the card's PCIe device: "0-23,48-71".
#
# Read from sysfs rather than parsed out of `nvidia-smi topo -m`, because sysfs
# answers for one card without a table to line up columns in, and it is the same
# list the driver's CPU Affinity column is derived from.
gpu_cpulist() {
  local index="$1" bus sysfs
  bus="$(nvidia-smi -i "$index" --query-gpu=pci.bus_id --format=csv,noheader)" \
    || die "could not read the PCI bus id of GPU $index"
  # nvidia-smi prints 00000000:3B:00.0; sysfs wants 0000:3b:00.0.
  sysfs="$(echo "$bus" | tr "A-Z" "a-z" | sed "s/^0000//")"
  [[ -r "/sys/bus/pci/devices/$sysfs/local_cpulist" ]] \
    || die "no local_cpulist for GPU $index at /sys/bus/pci/devices/$sysfs"
  cat "/sys/bus/pci/devices/$sysfs/local_cpulist"
}

# gpu_numa_node prints the NUMA node a GPU hangs off, or -1 if the host reports
# none.
gpu_numa_node() {
  local index="$1" bus sysfs
  bus="$(nvidia-smi -i "$index" --query-gpu=pci.bus_id --format=csv,noheader)"
  sysfs="$(echo "$bus" | tr "A-Z" "a-z" | sed "s/^0000//")"
  cat "/sys/bus/pci/devices/$sysfs/numa_node" 2>/dev/null || echo -1
}

# gpus_on_node prints, in index order, the GPUs sharing a NUMA node with the
# given one. It is what makes the thread split disjoint: a replica's share is
# decided by its position among its own node's cards, not by the card's index.
gpus_on_node() {
  local index="$1" node peers=() i fleet=()
  node="$(gpu_numa_node "$index")"
  # The fleet's own cards, named. Counting 0..REPLICA_COUNT-1 here would be wrong
  # twice over on this host: with GPU 3 excluded (#25) it would inspect a card the
  # fleet does not run on and miss GPU 5, which it does — so replica-5 would not
  # find itself among its own node's peers and would be given some other replica's
  # threads, or none.
  read -r -a fleet <<< "$REPLICA_GPUS"
  for i in "${fleet[@]}"; do
    if [[ "$(gpu_numa_node "$i")" == "$node" ]]; then peers+=("$i"); fi
  done
  echo "${peers[*]}"
}

# slice_threads prints replica `ordinal`'s share of a "0-23,48-71" cpu list:
# `want` threads, disjoint from every other replica on the same node.
#
# The share is taken evenly from EACH range rather than as the first n threads
# of the whole list, because on this host the two ranges are the node's physical
# cores and then their SMT siblings — cpu0's sibling is cpu48, cpu12's is cpu60.
# An even slice per range therefore gives every replica whole cores: six cores
# and those same six cores' siblings. Taking the first n would hand the early
# replicas twelve real cores and the later ones nothing but hyperthreads of
# cores already busy serving another replica.
slice_threads() {
  local list="$1" ordinal="$2" want="$3" siblings="$4"
  local parts=() out=() part low high span per_range start i
  IFS=',' read -r -a parts <<< "$list"
  (( ${#parts[@]} > 0 )) || die "GPU reports an empty cpu list"
  (( want % ${#parts[@]} == 0 )) \
    || die "$want threads per replica does not divide evenly into the ${#parts[@]} cpu ranges of: $list"
  per_range=$(( want / ${#parts[@]} ))
  for part in "${parts[@]}"; do
    low="${part%%-*}"
    high="${part##*-}"
    span=$(( high - low + 1 ))
    (( span >= per_range * siblings )) \
      || die "cpu range $part holds $span threads, too few for $siblings replicas at $per_range each. Lower CPU_THREADS_PER_REPLICA."
    start=$(( low + ordinal * per_range ))
    for (( i = 0; i < per_range; i++ )); do
      out+=( "$(( start + i ))" )
    done
  done
  local IFS=,
  echo "${out[*]}"
}

# pin_prefix prints the numactl invocation for a replica, or nothing when
# pinning is off.
#
# Both --physcpubind and --membind: binding cores without binding memory leaves
# a replica running on one node reading the other node's memory across the
# interconnect, which this host measures at roughly twice the local cost. The
# point of pinning is to make the six replicas equal, and half-pinning makes
# them less equal rather than more.
#
# The thread sets are disjoint across a node's replicas. Overlapping sets would
# be worse than no pinning at all: node 0's four cards would share one twelve
# thread set while node 1's two shared another, which is both less CPU than they
# have unpinned and still unequal between the nodes.
pin_prefix() {
  local index="$1" cpus node threads peers=() ordinal=-1 i
  [[ "$CPU_PINNING" == "1" ]] || return 0
  command -v numactl >/dev/null || die "CPU_PINNING is on but numactl is not installed"

  node="$(gpu_numa_node "$index")"
  [[ "$node" != "-1" ]] || die "GPU $index reports no NUMA node, so it cannot be pinned to one"

  read -r -a peers <<< "$(gpus_on_node "$index")"
  for i in "${!peers[@]}"; do
    if [[ "${peers[$i]}" == "$index" ]]; then ordinal="$i"; fi
  done
  (( ordinal >= 0 )) || die "GPU $index is not among the cards on its own NUMA node $node"

  cpus="$(gpu_cpulist "$index")"
  threads="$(slice_threads "$cpus" "$ordinal" "$CPU_THREADS_PER_REPLICA" "${#peers[@]}")"

  echo "numactl --physcpubind=$threads --membind=$node"
}

# kv_events_config prints the engine's --kv-events-config for one replica: its
# event publisher on KV_EVENTS_BASE_PORT + index, its replay socket on
# KV_EVENTS_REPLAY_BASE_PORT + index, and the replay buffer's depth. The router
# finds both through `ops/fleet.sh kv-events` and `kv-events-replay`.
#
# The endpoints say tcp://* on purpose. vLLM 0.28.0 binds an endpoint containing
# a wildcard and connects out to any other, so tcp://127.0.0.1 would have each
# engine dialling a router that is not listening, rather than listening for one.
kv_events_config() {
  local index="$1"
  printf '{"enable_kv_cache_events":true,"publisher":"zmq","endpoint":"tcp://*:%d","replay_endpoint":"tcp://*:%d","buffer_steps":%d,"topic":""}' \
    $(( KV_EVENTS_BASE_PORT + index )) $(( KV_EVENTS_REPLAY_BASE_PORT + index )) "$KV_EVENTS_BUFFER_STEPS"
}

# assert_forced_backend checks the engine actually took the quantization backend
# it was told to take. Auto-selection flipping between runs would change what
# the policy comparison measures without changing anything visible.
assert_forced_backend() {
  local log="$1"
  grep -Eiq "$QUANTIZATION_LOG_PATTERN" "$log" && return 0
  die "replica started but its log never confirmed the $QUANTIZATION backend.
     Checked $log against /$QUANTIZATION_LOG_PATTERN/i.
     Either the flag was ignored or the log wording changed; fix
     QUANTIZATION_LOG_PATTERN in ops/versions.env once you have confirmed which."
}

# engine_args sets ENGINE_ARGS to the vllm argv of the replica on one GPU:
# every pinned engine setting, read from versions.env and nowhere else. It is
# its own function so that anything else that has to start the same engine —
# the roofline's profiled replica (ops/roofline.sh, via `replica.sh args`) —
# asks for it here rather than restating it and drifting.
engine_args() {
  local index="$1"
  ENGINE_ARGS=(
    serve "$MODEL"
    --host 0.0.0.0
    --port "$(replica_port "$index")"
    --quantization "$QUANTIZATION"
    --linear-backend "$LINEAR_BACKEND"
    --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
    --max-num-seqs "$MAX_NUM_SEQS"
    --block-size "$BLOCK_SIZE"
    --max-model-len "$MAX_MODEL_LEN"
  )
  # Written as if/then, not `[[ ... ]] && ...`: under `set -e` a false test as
  # the whole statement would exit the script, so turning a knob off would kill
  # the launch instead of dropping the flag.
  if [[ "$ENABLE_PREFIX_CACHING" == "1" ]]; then
    ENGINE_ARGS+=(--enable-prefix-caching)
  fi
  if [[ "$ENABLE_CHUNKED_PREFILL" == "1" ]]; then
    ENGINE_ARGS+=(--enable-chunked-prefill)
  fi
  if [[ "$KV_CACHE_METRICS" == "1" ]]; then
    ENGINE_ARGS+=(--kv-cache-metrics)
  fi
  # Defaulted rather than bare, unlike the knobs above it. Those are all defined
  # in versions.env and have been since before this script could run; this one
  # arrives with a config change, and under `set -u` a replica.sh that reached
  # the box before its versions.env would kill every launch with an unbound
  # variable rather than simply not passing the flag.
  if [[ "${ENABLE_PROMPT_TOKENS_DETAILS:-0}" == "1" ]]; then
    ENGINE_ARGS+=(--enable-prompt-tokens-details)
  fi
  # KV cache events (#24), defaulted for the reason the flag above is.
  if [[ "${KV_EVENTS:-0}" == "1" ]]; then
    ENGINE_ARGS+=(--kv-events-config "$(kv_events_config "$index")")
  fi
}

up() {
  local index="$1"
  assert_index "$index"
  assert_engine_version

  local id port pid log
  id="$(replica_id "$index")"
  port="$(replica_port "$index")"
  pid="$(pid_file "$index")"
  log="$(log_file "$index")"

  mkdir -p "$run_dir"
  if [[ -f "$pid" ]] && kill -0 "$(cat "$pid")" 2>/dev/null; then
    die "$id is already running as PID $(cat "$pid")"
  fi

  engine_args "$index"
  local args=("${ENGINE_ARGS[@]}")

  local pin
  pin="$(pin_prefix "$index")"

  echo "starting $id on GPU $index, port $port, vLLM $VLLM_VERSION, quantization $QUANTIZATION, linear backend $LINEAR_BACKEND${pin:+, pinned: $pin}"
  # $pin is deliberately unquoted: it is either empty or a numactl invocation
  # with its own flags, and quoting it would make the whole thing one argv[0].
  # shellcheck disable=SC2086
  CUDA_VISIBLE_DEVICES="$index" \
  VLLM_USE_FLASHINFER_SAMPLER="$VLLM_USE_FLASHINFER_SAMPLER" \
    nohup $pin "$VENV/bin/vllm" "${args[@]}" >"$log" 2>&1 &
  echo $! >"$pid"

  local deadline=$(( SECONDS + STARTUP_TIMEOUT_SECONDS ))
  until curl -sf "http://127.0.0.1:$port/health" >/dev/null; do
    kill -0 "$(cat "$pid")" 2>/dev/null || die "$id exited during startup. Last lines of $log:
$(tail -n 20 "$log")"
    (( SECONDS < deadline )) || die "$id did not answer /health within ${STARTUP_TIMEOUT_SECONDS}s. See $log"
    sleep 2
  done

  assert_forced_backend "$log"
  echo "$id is up: http://127.0.0.1:$port (PID $(cat "$pid"))"
}

down() {
  local index="$1"
  assert_index "$index"
  local id pid
  id="$(replica_id "$index")"
  pid="$(pid_file "$index")"
  [[ -f "$pid" ]] || { echo "$id is not running"; return 0; }

  local target
  target="$(cat "$pid")"
  if kill -0 "$target" 2>/dev/null; then
    echo "stopping $id (PID $target)"
    kill "$target"
    for _ in $(seq 1 30); do
      kill -0 "$target" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$target" 2>/dev/null; then kill -9 "$target" || true; fi
  fi
  rm -f "$pid"
}

# descendants prints every process below a pid, children before grandchildren.
descendants() {
  local child
  for child in $(pgrep -P "$1" 2>/dev/null); do
    echo "$child"
    descendants "$child"
  done
}

# kill_hard does to a replica what a crash does: SIGKILL to the vLLM supervisor
# and to every process under it at once, with no chance to finish anything in
# flight. It is the chaos test's fault, and its difference from `down` is the
# experiment: `down` lets the engine exit cleanly, this does not.
#
# The whole tree rather than the pid in the pid file, because the process that
# holds the GPU is the supervisor's EngineCore child. Killing the supervisor
# alone would orphan the child with the card still full, and the replica
# restarted there would come up beside its own leftover or not at all. The tree
# is collected before anything is signalled, so no child is reparented between
# two kills and escapes the second.
kill_hard() {
  local index="$1"
  assert_index "$index"
  local id pid target tree p alive
  id="$(replica_id "$index")"
  pid="$(pid_file "$index")"
  [[ -f "$pid" ]] || die "$id has no pid file, so there is nothing to kill"
  target="$(cat "$pid")"
  kill -0 "$target" 2>/dev/null || die "$id (PID $target) is not running"

  tree="$target $(descendants "$target" | tr '\n' ' ')"
  echo "killing $id with SIGKILL: $tree"
  # shellcheck disable=SC2086
  kill -9 $tree 2>/dev/null || true
  # Waited out, so that whoever timestamps the fault starts the clock when the
  # replica is really gone rather than when it was asked to go.
  for _ in $(seq 1 50); do
    alive=0
    for p in $tree; do
      if kill -0 "$p" 2>/dev/null; then alive=1; fi
    done
    (( alive )) || break
    sleep 0.1
  done
  rm -f "$pid"
}

status() {
  shopt -s nullglob
  local any=0
  for pid in "$run_dir"/replica-*.pid; do
    any=1
    local id target state
    id="$(basename "$pid" .pid)"
    target="$(cat "$pid")"
    state="stopped"
    if kill -0 "$target" 2>/dev/null; then state="running"; fi
    echo "$id  PID $target  $state"
  done
  (( any )) || echo "no replicas have been started from $run_dir"
}

case "${1:-}" in
  up)     shift; up "${1:-}" ;;
  down)   shift; down "${1:-}" ;;
  kill)   shift; kill_hard "${1:-}" ;;
  status) status ;;
  # One argument per line: the argv `up` would start the engine with.
  args)   shift; assert_index "${1:-}"; engine_args "$1"; printf '%s\n' "${ENGINE_ARGS[@]}" ;;
  *)      die "usage: $0 up|down|kill|args <gpu index> | status" ;;
esac
