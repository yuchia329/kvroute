#!/usr/bin/env bash
# Assert that a replica's /tokenize gives the very tokens a chat completion of the
# same body prefills, before exact residency depends on it (#24, ADR-0010).
#
#   ops/probe-tokenize.sh [base-url]        # default http://127.0.0.1:8000
#
# Exact residency matches a prompt against the blocks an engine reports holding by
# the token ids /tokenize returns for the chat body. That is exact only if /tokenize
# renders the chat template and tokenizes exactly as the chat completion does, and a
# mismatch would not fail loudly: every match would drop to zero and read as an
# index that found nothing. So it is checked against the engine itself, on bodies
# shaped like the workload's -- a lone user turn, a system prompt, and a history
# with an assistant turn in it -- sent as the harness sends them, streaming fields
# and all, which /tokenize ignores.
#
# The strongest check the engine allows is made. Asked with return_token_ids, the
# chat completion echoes its prompt's token ids and the two lists must be equal.
# Where it does not echo them, the counts must agree with usage.prompt_tokens, and
# the probe says it fell back to the weaker check.
set -uo pipefail

BASE="${1:-http://127.0.0.1:8000}"
MODEL="${MODEL:-$(cd "$(dirname "$0")/.." && ./ops/fleet.sh env MODEL 2>/dev/null)}"
[ -n "$MODEL" ] || { echo "probe-tokenize: could not resolve MODEL; pass it in the environment" >&2; exit 2; }

echo "probe-tokenize: $BASE"
python3 - "$BASE" "$MODEL" <<'PY'
import json, sys, urllib.request

base, model = sys.argv[1], sys.argv[2]

def post(path, body):
    req = urllib.request.Request(base + path, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.load(r)

system = {"role": "system", "content": "You are a careful assistant who answers briefly. " * 8}
user = {"role": "user", "content": "the quick brown fox jumps over the lazy dog " * 40}
reply = {"role": "assistant", "content": "Here is what I found about that question. " * 16}
follow = {"role": "user", "content": "And what would change if the dog were quicker? " * 8}
cases = [
    ("user", [user]),
    ("system+user", [system, user]),
    ("multi-turn", [system, user, reply, follow]),
]

failed = False
for name, messages in cases:
    body = {"model": model, "messages": messages, "max_tokens": 1}
    # /tokenize is sent the body as the router forwards a harness request, stream
    # fields and all; the chat completion that checks it is asked not to stream,
    # so its answer arrives whole with the prompt's ids in it.
    tokenized = post("/tokenize", dict(body, stream=True, stream_options={"include_usage": True}))["tokens"]
    chat = post("/v1/chat/completions", dict(body, stream=False, return_token_ids=True))
    echoed = chat.get("prompt_token_ids")
    if echoed is None:
        echoed = ((chat.get("choices") or [{}])[0]).get("prompt_token_ids")
    prompt_tokens = (chat.get("usage") or {}).get("prompt_tokens")
    if echoed is not None:
        same = list(echoed) == list(tokenized)
        print("  %-12s /tokenize %5d tokens, chat prompt %5d tokens, ids %s" % (
            name, len(tokenized), len(echoed), "EQUAL" if same else "DIFFER"))
        if not same:
            first = next((i for i, (a, b) in enumerate(zip(tokenized, echoed)) if a != b), min(len(tokenized), len(echoed)))
            print("    first difference at token %d: /tokenize %s, chat %s" % (first, tokenized[first:first + 8], list(echoed)[first:first + 8]))
    else:
        same = prompt_tokens == len(tokenized)
        print("  %-12s /tokenize %5d tokens, usage.prompt_tokens %s — counts only: the engine did not echo its prompt ids" % (
            name, len(tokenized), prompt_tokens))
    failed = failed or not same

if failed:
    print("probe-tokenize: FAIL — /tokenize does not give the chat completion's own prompt tokens.")
    print("  Exact residency would match nothing and report it as an index that found nothing. Do not run it.")
    sys.exit(1)
print("probe-tokenize: OK — /tokenize gives the chat completion's own prompt tokens")
PY
