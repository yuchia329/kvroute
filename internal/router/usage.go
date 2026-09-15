package router

import (
	"encoding/json"
)

// usageTailBytes is how much of the end of a response is kept so that its usage
// block can be read back.
//
// The block is the last thing in both shapes the fleet serves: in a blocking
// completion vLLM writes it after the choices, and in a stream it arrives in the
// final data frame before [DONE]. It runs to a couple of hundred bytes. Eight
// kilobytes is two orders of magnitude of headroom against a field order that
// changes or a frame that carries more than expected, and it is a fixed cost per
// in-flight request rather than per byte streamed.
//
// A tail rather than the whole body, because the body is the measurement: a
// router that buffered responses to parse them would hold every answer in memory
// for as long as it took to send, and this project's whole load is streaming
// answers.
const usageTailBytes = 8 << 10

// usageTail keeps the last usageTailBytes of a response as it streams past.
//
// Not a ring: the tail is read once, at the end, and a straight copy-and-shift
// over a buffer this size costs less than the bookkeeping to avoid it. Nothing
// here is on the path between a replica emitting a token and the client seeing
// it — the write to the client and its flush happen first.
type usageTail struct {
	buf []byte
}

func (t *usageTail) Write(p []byte) {
	if len(p) >= usageTailBytes {
		t.buf = append(t.buf[:0], p[len(p)-usageTailBytes:]...)
		return
	}
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - usageTailBytes; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
	}
}

// engineUsage is the engine's own account of one request's prompt: how many
// tokens it came to, and how many of them the replica answered out of its KV
// cache instead of prefilling.
//
// This is the ground truth the router's prefix match is a prediction of
// (ADR-0008), and holding the two against each other per request is what the
// honoured rate is made of.
type engineUsage struct {
	PromptTokens int
	CachedTokens int
}

// usageBlock is the shape of what is parsed out of the tail. The details are a
// pointer because their absence is meaningful: a replica started without
// --enable-prompt-tokens-details reports usage and no breakdown, and a replica
// that served nothing out of cache reports a breakdown of zero. Reading the
// first as the second would score every such replica as honouring nothing, and
// spill the fleet apart on a missing engine flag.
type usageBlock struct {
	PromptTokens        int `json:"prompt_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// readUsage pulls the engine's account of the prompt out of the tail of a
// response, and reports whether the tail carried one that can be scored
// against.
//
// The last usage object in the tail, because a stream's earlier frames carry
// "usage": null and the real one arrives at the end. Scanned rather than decoded
// whole: the tail begins mid-document by construction, so there is no JSON value
// here to unmarshal — only a known object embedded in one.
//
// Everything it cannot make sense of is reported as no usage rather than as a
// zero. A response the client never asked usage for, a replica that does not
// break the prompt down, a body that ended early, a tail too short to hold the
// whole object: each of them means this request cannot say anything about what
// its replica was holding, and the honoured rate must not hear from it at all.
func readUsage(tail []byte) (engineUsage, bool) {
	body, ok := lastJSONObject(tail, `"usage"`)
	if !ok {
		return engineUsage{}, false
	}
	var block usageBlock
	if err := json.Unmarshal(body, &block); err != nil {
		return engineUsage{}, false
	}
	if block.PromptTokens <= 0 || block.PromptTokensDetails == nil {
		return engineUsage{}, false
	}
	return engineUsage{PromptTokens: block.PromptTokens, CachedTokens: block.PromptTokensDetails.CachedTokens}, true
}

// lastJSONObject returns the object value of the last occurrence of key in
// body, and whether one was found complete.
//
// Brace-counting with string awareness rather than a regular expression,
// because the object holds nested objects and the strings around it hold
// braces: a model's own output sits a few hundred bytes earlier in this same
// tail, and it can contain anything at all.
func lastJSONObject(body []byte, key string) ([]byte, bool) {
	for at := lastIndex(body, key); at >= 0; at = lastIndex(body[:at], key) {
		rest := body[at+len(key):]
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
			i++
		}
		if i >= len(rest) || rest[i] != ':' {
			continue
		}
		i++
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
			i++
		}
		if i >= len(rest) || rest[i] != '{' {
			// "usage": null, which every frame of a stream but the last one
			// carries. Keep looking further back rather than giving up: the tail
			// is scanned from the end, so the first complete object found is the
			// one the engine finished with.
			continue
		}
		if end, closed := objectEnd(rest[i:]); closed {
			return rest[i : i+end], true
		}
	}
	return nil, false
}

// objectEnd returns one past the brace that closes the object starting at
// body[0], and whether the object was closed inside body at all.
func objectEnd(body []byte) (int, bool) {
	depth, inString, escaped := 0, false, false
	for i, c := range body {
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// lastIndex is bytes.LastIndex for a string needle, spelled here so the scan
// above reads as one loop rather than two conversions.
func lastIndex(body []byte, needle string) int {
	for i := len(body) - len(needle); i >= 0; i-- {
		if string(body[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}
