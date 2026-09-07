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

  echo "starting $id on GPU $index, port $port, vLLM $VLLM_VERSION, quantization $QUANTIZATION"
  CUDA_VISIBLE_DEVICES="$index" nohup "$VENV/bin/vllm" "${args[@]}" >"$log" 2>&1 &
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
