#!/usr/bin/env bash
#
# Supervise one vLLM replica as a bare process.
#
# The box has no container runtime and no permission to install one, which suits
# this project anyway: the chaos test kills a specific process, and killing a
# bare PID is lower-latency and less noisy than killing a container.
#
#   ops/replica.sh up 0       # start replica-0 on GPU 0, port 8000
#   ops/replica.sh down 0
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

# take_threads prints the first n threads of a "0-23,48-71" list, comma
# separated. Which n does not matter as long as every replica gets the same
# count; that they are contiguous and local is what makes them the right ones.
take_threads() {
  local list="$1" want="$2" parts=() out=() part low high cpu
  IFS=',' read -r -a parts <<< "$list"
  for part in "${parts[@]}"; do
    low="${part%%-*}"
    high="${part##*-}"
    for (( cpu = low; cpu <= high && ${#out[@]} < want; cpu++ )); do
      out+=("$cpu")
    done
  done
  (( ${#out[@]} == want )) || die "GPU has only ${#out[@]} threads local to it, fewer than the $want asked for: $list"
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
pin_prefix() {
  local index="$1" cpus node threads
  [[ "$CPU_PINNING" == "1" ]] || return 0
  command -v numactl >/dev/null || die "CPU_PINNING is on but numactl is not installed"

  node="$(gpu_numa_node "$index")"
  [[ "$node" != "-1" ]] || die "GPU $index reports no NUMA node, so it cannot be pinned to one"
  cpus="$(gpu_cpulist "$index")"
  threads="$(take_threads "$cpus" "$CPU_THREADS_PER_REPLICA")"

  echo "numactl --physcpubind=$threads --membind=$node"
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

  local args=(
    serve "$MODEL"
    --host 0.0.0.0
    --port "$port"
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
    args+=(--enable-prefix-caching)
  fi
  if [[ "$ENABLE_CHUNKED_PREFILL" == "1" ]]; then
    args+=(--enable-chunked-prefill)
  fi
  if [[ "$KV_CACHE_METRICS" == "1" ]]; then
    args+=(--kv-cache-metrics)
  fi

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
  status) status ;;
  *)      die "usage: $0 up|down <gpu index> | status" ;;
esac
