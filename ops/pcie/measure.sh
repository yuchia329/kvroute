#!/usr/bin/env bash
# #22 on the box: measure what idea.md §8's arithmetic needs. Run from ~/kvroute:
#
#   ./ops/pcie/measure.sh runs/pcie     # ~15 min; needs all six cards free, leaves them free
#
# Two measurements, both on an otherwise idle box:
#
#  1. How fast KV-sized buffers move between cards: every ordered pair, CUDA's
#     own peer copy and a pipelined bounce through pinned host memory; alone,
#     with both cards busy, and every pair of one class at once. Run twice, with
#     the host buffer bound to each NUMA node in turn, because a SYS pair's
#     bounce crosses the socket link on one leg whichever node holds the buffer.
#  2. How long one replica takes to prefill N tokens, at the lengths whose KV the
#     first measurement moved.
#
# LENGTHS and SIZES_MIB are the same requests seen from each side: prompt
# lengths in tokens, and the KV those tokens occupy at 128 KiB each (32 layers x
# 8 KV heads x 128 dims x 2 bytes x K and V). cmd/disagg derives the 128 KiB
# from the model's own config.json and refuses a run in which some length's KV
# size was not measured, so the two lists cannot drift apart unnoticed.
source ops/versions.env
set -euo pipefail

OUT="${1:?usage: ops/pcie/measure.sh <dir>}"
LENGTHS="256,512,1024,2048,4096,7936"
SIZES_MIB="32,64,128,256,512,992"
# The one size measured busy and together: 2,048 tokens, a whole session of the
# frozen workload.
LOAD_MIB=256
# The prefill card. Any card of the fleet will do, since driven alone the six
# agree within 1.5%, but not GPU 3, which is not in the fleet.
PREFILL_GPU="${PREFILL_GPU:-1}"
PY="$VENV/bin/python"

mkdir -p "$OUT"
say() { echo "$(date -u +%FT%TZ) $*" | tee -a "$OUT/measure.log"; }

nvidia-smi topo -m > "$OUT/topology.txt"
nvidia-smi topo -p2p r > "$OUT/p2p-read.txt"
nvidia-smi topo -p2p w > "$OUT/p2p-write.txt"
python3 ops/pcie/tree.py > "$OUT/pcie-tree.txt"
config="$("$PY" -c 'import sys; from huggingface_hub import hf_hub_download as d; print(d(sys.argv[1], "config.json", local_files_only=True))' "$MODEL")"
cp "$config" "$OUT/model-config.json"

# bandwidth.py checks all six cards itself: fleet.sh preflight checks only the
# fleet's five, and these copies touch GPU 3 too.
for node in 0 1; do
  say "bandwidth, host buffer on NUMA node $node"
  numactl --cpunodebind="$node" --membind="$node" \
    "$PY" ops/pcie/bandwidth.py --dir "$OUT/bandwidth-node$node" --host-node "$node" \
      --sizes-mib "$SIZES_MIB" --load-mib "$LOAD_MIB" >> "$OUT/measure.log" 2>&1
done

# Brought down however this ends, so the box is left as it was found.
trap './ops/replica.sh down "$PREFILL_GPU" >> "$OUT/measure.log" 2>&1 || true' EXIT
./ops/fleet.sh preflight
say "prefill on replica-$PREFILL_GPU"
./ops/replica.sh up "$PREFILL_GPU" >> "$OUT/measure.log" 2>&1
"$PY" ops/pcie/prefill.py --replica "http://127.0.0.1:$((BASE_PORT + PREFILL_GPU))" --model "$MODEL" \
  --lengths "$LENGTHS" --out "$OUT/prefill.jsonl" >> "$OUT/measure.log" 2>&1
cp "run/replica-$PREFILL_GPU.log" "$OUT/engine.log"
./ops/replica.sh down "$PREFILL_GPU" >> "$OUT/measure.log" 2>&1
trap - EXIT

nvidia-smi --query-gpu=index,memory.used --format=csv >> "$OUT/measure.log"
say "done"
