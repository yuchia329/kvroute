#!/usr/bin/env bash
#
# Profile one replica's own prefill and decode steps under Nsight Systems, for
# the roofline (#23).
#
#   ops/roofline.sh [out dir]     # default runs/roofline; about ten minutes
#
# Runs on the GPU box from ~/kvroute, like the fleet scripts, and needs every
# card idle: it
#
#   1. refuses to start unless all six cards are idle. A roofline taken beside
#      someone else's load measures the share of the card it was given, and the
#      host's other five cards are what that load would be on;
#   2. measures the card's two ceilings with library calls — a large fp16 GEMM
#      and a large device-to-device copy — so the roofs are this card's under
#      its own clocks and power cap, with the datasheet's beside them;
#   3. starts one replica with the pinned engine (`ops/replica.sh args`) under
#      nsys, with vLLM's cuda profiler annotating every engine step;
#   4. drives the plan inside one profile window (`roofline drive`), then stops
#      the replica so nsys writes its report;
#   5. exports the step ranges and every GPU operation as CSV, which is all
#      `roofline report` reads.
#
# nvidia-smi samples all six cards throughout, so the run carries its own
# evidence of clocks, throttling and anything else that appeared on the box.
#
# No kernel is written. The ceilings are cuBLAS and a copy; the steps are vLLM's.
# Nsight Compute is not used: this driver reserves the GPU's performance
# counters for root (RmProfilingAdminOnly=1), so the steps' work is counted from
# the model instead of read off the counters — see internal/roofline.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(dirname "$here")"
# shellcheck source=versions.env
source "$here/versions.env"
cd "$repo"

out="${1:-runs/roofline}"
# GPU 1: a fleet card on NUMA node 0, steady under load. Not GPU 3, which
# throttles thermally (#25), nor GPU 0, the slowest of the five the fleet keeps.
gpu="${ROOFLINE_GPU:-1}"
nsys="${NSYS:-/usr/local/bin/nsys}"
all_gpus="0 1 2 3 4 5"
port=$(( BASE_PORT + gpu ))

# Every step annotated with its composition — the per-request sums the work is
# counted from. The profiler only records between /start_profile and
# /stop_profile, and nsys only captures between the two CUDA profiler calls
# those make.
profiler_config='{"profiler":"cuda","detailed_trace_annotation":true}'

die() { echo "roofline: $*" >&2; exit 1; }
say() { echo "[$(date +%T)] $*"; }

[[ -x "$nsys" ]] || die "no nsys at $nsys; set NSYS"
[[ -x bin/roofline-linux-amd64 ]] || die "no bin/roofline-linux-amd64. Build it with 'make linux' and copy bin/ across."
mkdir -p "$out"

say "preflight: all six cards must be idle"
bin/preflight-linux-amd64 -gpu-indexes "$all_gpus" -threshold-mib "$GPU_DIRTY_THRESHOLD_MIB" \
  || die "preflight refused. The roofline wants the whole box; wait for it to clear."

app_pid() { pgrep -u "$USER" -f "^$VENV/bin/python $VENV/bin/vllm serve .*--port $port( |$)" || true; }
telemetry_pid=""
cleanup() {
  local p
  p="$(app_pid)"
  if [[ -n "$p" ]]; then kill -TERM $p 2>/dev/null || true; fi
  if [[ -n "$telemetry_pid" ]]; then kill "$telemetry_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT

nvidia-smi --query-gpu=timestamp,index,memory.used,utilization.gpu,clocks.sm,clocks.mem,power.draw,temperature.gpu,clocks_throttle_reasons.active \
  --format=csv,nounits -lms 500 > "$out/gpu-telemetry.csv" &
telemetry_pid=$!

say "ceilings: fp16 GEMM and device copy on GPU $gpu"
CUDA_VISIBLE_DEVICES="$gpu" "$VENV/bin/python" - > "$out/ceilings.json" <<'PY'
# The card's two roofs, measured with library calls rather than taken from the
# datasheet: the rate its tensor cores reach on a large fp16 GEMM (cuBLAS, fp32
# accumulate — what Marlin accumulates in too) and the rate its memory reaches
# on a large copy (each byte read once and written once). The best of several
# trials, because a ceiling is what the card can do, not what it usually does.
import json, torch

def seconds(fn, iters):
    for _ in range(3):
        fn()
    torch.cuda.synchronize()
    start, end = torch.cuda.Event(enable_timing=True), torch.cuda.Event(enable_timing=True)
    start.record()
    for _ in range(iters):
        fn()
    end.record()
    torch.cuda.synchronize()
    return start.elapsed_time(end) / 1e3 / iters

n = 8192
a, b = (torch.randn(n, n, device="cuda", dtype=torch.float16) for _ in range(2))
c = torch.empty(n, n, device="cuda", dtype=torch.float16)
gemm = [2 * n**3 / seconds(lambda: torch.matmul(a, b, out=c), 20) for _ in range(5)]

elements = 1 << 29  # 1 GiB of fp16
x = torch.empty(elements, device="cuda", dtype=torch.float16)
y = torch.empty_like(x)
copy = [2 * 2 * elements / seconds(lambda: y.copy_(x), 20) for _ in range(5)]

print(json.dumps({
    "card": torch.cuda.get_device_name(),
    "compute_flops_per_second": max(gemm),
    "bandwidth_bytes_per_second": max(copy),
    "gemm_trials_flops_per_second": gemm,
    "copy_trials_bytes_per_second": copy,
    "gemm": f"fp16 {n}x{n}x{n}, torch.matmul",
    "copy": f"{2 * elements} bytes, Tensor.copy_",
    "torch": torch.__version__,
}, indent=2))
PY
say "ceilings: $(tr -d '\n ' < "$out/ceilings.json" | cut -c1-160)"

"$here/replica.sh" args "$gpu" > "$out/engine-args.txt"
mapfile -t engine < "$out/engine-args.txt"

say "starting the profiled replica on GPU $gpu, port $port, under nsys"
CUDA_VISIBLE_DEVICES="$gpu" \
VLLM_USE_FLASHINFER_SAMPLER="$VLLM_USE_FLASHINFER_SAMPLER" \
  "$nsys" profile --trace=cuda,nvtx --cuda-graph-trace=node \
    --capture-range=cudaProfilerApi --capture-range-end=stop \
    --force-overwrite=true --output="$out/steps" \
    "$VENV/bin/vllm" "${engine[@]}" --profiler-config "$profiler_config" \
    > "$out/serve.log" 2>&1 &
nsys_pid=$!

deadline=$(( SECONDS + STARTUP_TIMEOUT_SECONDS ))
until curl -sf "http://127.0.0.1:$port/health" >/dev/null; do
  kill -0 "$nsys_pid" 2>/dev/null || die "the replica exited during startup. Last lines of $out/serve.log:
$(tail -n 20 "$out/serve.log")"
  (( SECONDS < deadline )) || die "the replica did not answer /health within ${STARTUP_TIMEOUT_SECONDS}s. See $out/serve.log"
  sleep 2
done
grep -Eiq "$QUANTIZATION_LOG_PATTERN" "$out/serve.log" \
  || die "the replica's log never confirmed the $QUANTIZATION backend (/$QUANTIZATION_LOG_PATTERN/i)"
say "replica up"

say "driving the plan"
bin/roofline-linux-amd64 drive -url "http://127.0.0.1:$port" -model "$MODEL" ${ROOFLINE_DRIVE_ARGS:-} 2>&1 | tee "$out/drive.log"

say "stopping the replica; nsys writes its report"
kill -TERM "$(app_pid)"
for _ in $(seq 1 300); do
  kill -0 "$nsys_pid" 2>/dev/null || break
  sleep 1
done
kill -0 "$nsys_pid" 2>/dev/null && die "nsys was still running 300 s after the replica was stopped"
kill "$telemetry_pid" 2>/dev/null || true
telemetry_pid=""

say "exporting step ranges and GPU operations"
"$nsys" stats --quiet --format csv --report nvtx_gpu_proj_trace,cuda_gpu_trace \
  --output "$out/steps" "$out/steps.nsys-rep"

snapshot="$(ls -d "$HOME/.cache/huggingface/hub/models--${MODEL//\//--}/snapshots/"*/ | head -n 1)"
cp "${snapshot}config.json" "$out/model-config.json"
{
  echo "date: $(date -Is)"
  echo "host: $(hostname)"
  echo "gpu: $gpu"
  echo "cpu pinning: $CPU_PINNING"
  echo "vllm: $("$VENV/bin/python" -c 'import vllm; print(vllm.__version__)')"
  echo "nsys: $("$nsys" --version)"
  echo "driver: $(nvidia-smi --query-gpu=driver_version --format=csv,noheader -i "$gpu")"
  echo "profiler config: $profiler_config"
  echo "drive args: ${ROOFLINE_DRIVE_ARGS:-(defaults)}"
} > "$out/environment.txt"

# Anything that appeared on another card while this ran shared the host with
# it. Not fatal — the trace is still the trace — but the run says so.
foreign="$(awk -F', ' -v gpu="$gpu" -v limit="$GPU_DIRTY_THRESHOLD_MIB" \
  'NR > 1 && $2 != gpu && $3 + 0 > limit { n++ } END { print n + 0 }' "$out/gpu-telemetry.csv")"
if (( foreign > 0 )); then
  say "WARNING: $foreign telemetry samples show another card holding memory while this ran"
fi
echo "foreign samples on other cards: $foreign" >> "$out/environment.txt"

say "done: $out"
