#!/usr/bin/env bash
# Derive the prefix index's TTL from the engines' own idle-before-evict tail.
#
#   ops/derive-ttl.sh [out.json]        # default runs/prefix-calibration-derived.json
#
# The TTL is a claim about how long a replica goes on holding a block nobody has
# asked for, and ADR-0006 insists it be measured rather than chosen. The engine
# publishes the answer as vllm:kv_block_idle_before_evict_seconds, but the family
# is EMPTY until blocks have actually been evicted -- a fleet that has just come
# up has nothing to say -- and it is cleared by a restart.
#
# That is the whole difficulty, and it is what the 2026-09-09 definitive run got
# wrong: its fleet was cycled between the last baseline and the calibration, so
# calibrate read a fleet that had been up for four minutes, found the histogram
# empty, and fell back to a chosen 20 s. Prefix affinity then ran a whole pass
# against a guess. This script exists so the derivation is a thing that can be
# run deliberately, on its own, rather than a step buried in a nine-hour run
# where its failure is one line in a log.
#
# It therefore does two things in one breath and NEVER restarts the fleet in
# between: it puts the fleet under enough load to force eviction, then scrapes
# the tail that load produced.
set -uo pipefail
cd "$(dirname "$0")/.."

OUT="${1:-runs/prefix-calibration-derived.json}"
FROM="${FROM:-runs/definitive/concurrency}"
CONCURRENCY="${CONCURRENCY:-32}"
# Ten minutes, not two. Idle-before-evict describes how fast a cache turns over,
# and the first evictions after a bring-up are the cache filling rather than the
# steady state it will spend a sweep in. Long enough to get past that, short
# enough to be a step rather than a run.
LOAD="${LOAD:-600s}"
BIN="${BIN:-./bin}"
PORT="${PORT:-8080}"

# The frozen workload. Eviction is what is wanted here rather than a comparable
# cell, but driving the fleet with the same bytes the comparison uses is what
# makes the resulting tail describe the fleet the comparison ran on.
WORKLOAD="${WORKLOAD:--workload multiturn -sessions 307 -turns-per-session 4 -prompt-tokens 448 \
 -output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -skew 0 -seed 1}"

say() { echo "[$(date -u +%H:%M:%S)] $*"; }
die() { echo "derive-ttl: $*" >&2; exit 1; }

SPECS="$(./ops/fleet.sh replicas)" || die "could not read the fleet's replica specs"
MODEL="$(./ops/fleet.sh env MODEL)" || die "could not read MODEL"
GPUS="$(./ops/fleet.sh env REPLICA_GPUS)" || die "could not read REPLICA_GPUS"
[ -n "$SPECS" ] || die "the fleet reports no replicas; bring it up first"
[ -d "$FROM" ] || die "no sweep at $FROM to measure the prompt bytes-per-token ratio from; set FROM="

say "fleet: $SPECS"

# observations sums the idle-before-evict count across the whole fleet, and says
# whether it could read every replica.
#
# Three bugs are designed out of it, all of which the first version had.
#
# The URL is built from one spec rather than from the whole comma-separated
# list: "${SPECS#*=}" strips to the FIRST "=" and keeps every replica after it,
# which produced http://http://127.0.0.1:8000,replica-1=... — a URL curl cannot
# fetch. It failed silently, the reading defaulted to zero, and the script
# reported a fleet that had evicted nothing while 5,076 observations sat on it.
#
# It pools every replica rather than trusting replica-0, because that is what
# the calibration itself does and one card is not the fleet.
#
# And it distinguishes "scraped and found none" from "could not scrape", which
# is the same distinction vllmmetrics.KVUtilization draws and for the same
# reason: a failed scrape reported as a zero is a measurement invented out of an
# absence. It prints "unread" for that, and the caller refuses rather than
# telling anyone to run more load.
observations() {
  local total=0 spec base n unread=0
  local IFS=,
  for spec in $SPECS; do
    base="${spec#*=}"
    n="$(curl -sf --max-time 10 "$base/metrics" 2>/dev/null \
      | awk '/^vllm:kv_block_idle_before_evict_seconds_count\{/ {print $2; exit}')"
    if [ -z "$n" ]; then unread=$((unread + 1)); continue; fi
    total="$(awk -v a="$total" -v b="$n" 'BEGIN{printf "%d", a + b}')"
  done
  if [ "$unread" -gt 0 ]; then printf 'unread:%d:%d' "$unread" "$total"; else printf '%d' "$total"; fi
}

# A fleet that has been up a while may already have a tail, in which case the
# load below only deepens it. Reported either way, because a derivation that
# rests on a fleet somebody happened to have been using is worth knowing about.
before="$(observations)"
say "idle-before-evict observations across the fleet before load: $before"

# Deliberately not a scratch directory the exit trap removes. The failure worth
# reading is a load pass that would not start, and the first version of this
# script deleted exactly that log on its way out.
TMP="${TMP:-runs/derive-ttl}"
mkdir -p "$TMP"
trap 'kill "${RPID:-}" 2>/dev/null' EXIT

say "starting a least-outstanding router on 127.0.0.1:$PORT"
"$BIN/router-linux-amd64" -listen "127.0.0.1:$PORT" -replicas "$SPECS" \
  -policy least_outstanding > "$TMP/router.log" 2>&1 &
RPID=$!
sleep 3
curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null || { tail -20 "$TMP/router.log"; die "router did not come up"; }

# Load past capacity, so blocks are evicted rather than merely held. The
# workload offers well above the fleet's KV either way -- ADR-0007 records that
# its stated working set of 1.0 is really nearer 2.5 -- so what this buys is
# time under that pressure, not a bigger pool.
say "driving the fleet for $LOAD at concurrency $CONCURRENCY to force eviction"
"$BIN/bench-linux-amd64" -router "http://127.0.0.1:$PORT" -dir "$TMP/load" \
  -policy least_outstanding -replicas "$SPECS" -model "$MODEL" -gpu-indexes "$GPUS" \
  $WORKLOAD -concurrency "$CONCURRENCY" -cell-duration "$LOAD" -warmup 0s \
  -settle 0s -repetitions 1 > "$TMP/bench.log" 2>&1
status=$?
say "load pass exited $status"
[ "$status" -eq 0 ] || { tail -5 "$TMP/bench.log"; die "the load pass did not run, so nothing was evicted. Its log is $TMP/bench.log"; }

kill "$RPID" 2>/dev/null; wait "$RPID" 2>/dev/null; RPID=""
say "router drained"

after="$(observations)"
say "idle-before-evict observations across the fleet after load: $after"
case "$after" in
  unread:*)
    die "could not scrape $(echo "$after" | cut -d: -f2) of the fleet's replicas, so what they evicted is unknown.
  That is not the same as a fleet that evicted nothing, and calibrating on the replicas that did answer
  would size the index against part of a fleet. Fix the scrape -- do NOT restart, which clears what is there." ;;
  0)
    die "the fleet evicted nothing, so there is still no tail to calibrate against.
  Either the replicas lack --kv-cache-metrics, or the load did not exceed KV capacity.
  Raise CONCURRENCY or LOAD and try again -- do NOT restart the fleet, which clears what is there." ;;
esac

# The fleet is deliberately NOT restarted here. Everything above exists to fill
# these histograms and a bring-up empties them.
say "deriving the TTL, without touching the fleet"
"$BIN/calibrate-linux-amd64" -replicas "$SPECS" -from "$FROM" -out "$OUT" || die "calibrate failed"

say "wrote $OUT"
python3 - "$OUT" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
idle = c.get("block_idle_before_evict", {})
print()
print(f"  fleet tokens           {c.get('fleet_tokens')}")
print(f"  prompt bytes per token {c.get('prompt_bytes_per_token', 0):.2f}")
chosen = c.get("chosen_ttl")
print(f"  TTL source             {'CHOSEN — the derivation still failed' if chosen else 'measured'}")
counts, bounds = idle.get("cumulative") or [], idle.get("bounds") or []
total = idle.get("count", 0)
print(f"  idle-before-evict      {total} observations")
if total and counts and bounds:
    print()
    print("  how long blocks sat idle before the engine dropped them:")
    prev = 0
    for b, cum in zip(bounds, counts):
        n = cum - prev; prev = cum
        if n:
            print(f"    <= {b:>7}s  {n:8.0f}  ({100*cum/total:5.1f}% cumulative)")
    print()
    print("  The run of 2026-09-09 used a CHOSEN 20s. Read the cumulative column at 20s:")
    print("  that is the share of blocks already gone by the time the index stopped believing.")
PY
