#!/usr/bin/env bash
# Assert a replica reports per-request cached prompt tokens, before a sweep
# depends on it.
#
#   ops/probe-usage.sh [base-url]        # default http://127.0.0.1:8000
#
# Belief divergence (#17) is the gap between what the router predicted the
# chosen replica held and what the engine says it actually reused. The engine's
# side of that is usage.prompt_tokens_details.cached_tokens, and it is null
# unless the replica was started with --enable-prompt-tokens-details:
# _make_prompt_tokens_details returns None before it looks at anything else, so
# asking for it on the request buys nothing. vllm:request_prefill_kv_computed_tokens
# is a histogram and cannot be joined to a request, so there is no other source.
#
# Run this at bring-up, before the first cell. The failure it exists to catch is
# silent: a sweep against a fleet without the flag records a divergence column
# of nulls and looks exactly like a sweep that worked, hours later.
#
# It sends the same prompt twice. The first request proves the field is
# published at all; the second proves it reports reuse, because prefix caching
# is on and the replica has just seen these exact bytes. A field that is present
# and always zero would pass a weaker check and measure nothing.
set -uo pipefail

BASE="${1:-http://127.0.0.1:8000}"
MODEL="${MODEL:-$(cd "$(dirname "$0")/.." && ./ops/fleet.sh env MODEL 2>/dev/null)}"
[ -n "$MODEL" ] || { echo "probe-usage: could not resolve MODEL; pass it in the environment" >&2; exit 2; }

PROMPT="$(python3 -c 'print("the quick brown fox jumps over the lazy dog " * 60)')"
body() {
  python3 -c '
import json,sys
print(json.dumps({
  "model": sys.argv[1],
  "max_tokens": 8,
  "stream": False,
  "messages": [{"role": "user", "content": sys.argv[2]}],
}))' "$MODEL" "$PROMPT"
}

ask() {
  curl -sf -X POST "$BASE/v1/chat/completions" \
    -H 'content-type: application/json' -d "$(body)" 2>/dev/null
}

read_usage() {
  python3 -c '
import json,sys
try:
    r = json.load(sys.stdin)
except Exception as e:
    print("PARSE_ERROR", e); sys.exit(3)
u = r.get("usage") or {}
d = u.get("prompt_tokens_details")
if d is None:
    print("ABSENT", u.get("prompt_tokens"))
    sys.exit(4)
print("PRESENT", u.get("prompt_tokens"), d.get("cached_tokens"))'
}

echo "probe-usage: $BASE"

first="$(ask)"  || { echo "probe-usage: FAIL — $BASE did not answer" >&2; exit 1; }
out="$(printf '%s' "$first" | read_usage)"; status=$?
if [ "$status" -eq 4 ]; then
  echo "probe-usage: FAIL — prompt_tokens_details is null." >&2
  echo "  The replica was started without --enable-prompt-tokens-details." >&2
  echo "  Set ENABLE_PROMPT_TOKENS_DETAILS=1 in ops/versions.env and restart the fleet." >&2
  echo "  Do NOT enable it mid-sweep: it changes the engine configuration every cell shares." >&2
  exit 1
fi
[ "$status" -eq 0 ] || { echo "probe-usage: FAIL — could not read usage: $out" >&2; exit 1; }
echo "  cold  → $out"

second="$(ask)" || { echo "probe-usage: FAIL — second request did not answer" >&2; exit 1; }
out2="$(printf '%s' "$second" | read_usage)" || { echo "probe-usage: FAIL — $out2" >&2; exit 1; }
echo "  warm  → $out2"

cached="$(printf '%s' "$out2" | awk '{print $3}')"
case "$cached" in
  ''|None|null)
    echo "probe-usage: FAIL — the field is published but carries no cached_tokens." >&2
    exit 1;;
esac
if [ "$cached" -gt 0 ] 2>/dev/null; then
  echo "probe-usage: OK — cached_tokens reported, and the warm request reused $cached tokens"
  exit 0
fi
echo "probe-usage: FAIL — the warm request reused 0 tokens." >&2
echo "  The field works but the prefix cache did not hit on bytes it had just seen." >&2
echo "  Check ENABLE_PREFIX_CACHING before trusting any divergence figure." >&2
exit 1
