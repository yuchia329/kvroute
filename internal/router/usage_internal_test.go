package router

import (
	"strings"
	"testing"
)

func tailOf(chunks ...string) []byte {
	var t usageTail
	for _, c := range chunks {
		t.Write([]byte(c))
	}
	return t.buf
}

// The blocking shape: usage sits at the end of one JSON document.
func TestTheEnginesAccountIsReadOffABlockingResponse(t *testing.T) {
	body := `{"id":"x","choices":[{"message":{"content":"hello"}}],` +
		`"usage":{"prompt_tokens":900,"completion_tokens":12,"total_tokens":912,` +
		`"prompt_tokens_details":{"cached_tokens":768}}}`

	got, ok := readUsage(tailOf(body))
	if !ok {
		t.Fatal("a complete usage block was not read")
	}
	if got.PromptTokens != 900 || got.CachedTokens != 768 {
		t.Errorf("usage = %+v, want 900 prompt tokens of which 768 cached", got)
	}
}

// The streaming shape, which is what every cell of the comparison actually
// sends. Every frame but the last carries "usage": null, so the last complete
// object is the only one worth reading.
func TestTheEnginesAccountIsReadOffTheLastFrameOfAStream(t *testing.T) {
	got, ok := readUsage(tailOf(
		"data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}],\"usage\":null}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}],\"usage\":null}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":512,\"prompt_tokens_details\":{\"cached_tokens\":256}}}\n\n",
		"data: [DONE]\n\n",
	))
	if !ok {
		t.Fatal("the final frame's usage block was not read")
	}
	if got.PromptTokens != 512 || got.CachedTokens != 256 {
		t.Errorf("usage = %+v, want 512 prompt tokens of which 256 cached", got)
	}
}

// A model's own output is in the same tail and can contain anything, braces and
// quoted braces included. The scan has to be a parser rather than a search.
func TestABodyFullOfBracesDoesNotConfuseTheScan(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"{\"usage\": {\"prompt_tokens\": 1}}"}}],"usage":null}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":64,"prompt_tokens_details":{"cached_tokens":32}}}` + "\n\n"

	got, ok := readUsage(tailOf(body))
	if !ok || got.PromptTokens != 64 || got.CachedTokens != 32 {
		t.Errorf("usage = %+v (%v), want the engine's 64/32 rather than the model's output", got, ok)
	}
}

// A replica started without --enable-prompt-tokens-details reports usage and no
// breakdown. Reading that as a breakdown of zero would score the replica as
// honouring nothing and spill the fleet apart on a missing engine flag.
func TestUsageWithoutTheCachedBreakdownIsNotScored(t *testing.T) {
	body := `{"usage":{"prompt_tokens":900,"completion_tokens":12,"total_tokens":912}}`

	if got, ok := readUsage(tailOf(body)); ok {
		t.Errorf("usage without a cached-token breakdown was scored as %+v", got)
	}
}

// A cached count of zero is a real answer and has to be scored: it is what a
// replica that evicted the whole prompt reports, which is exactly the state the
// spill rule exists to notice.
func TestACachedCountOfZeroIsAnAnswerRatherThanASilence(t *testing.T) {
	body := `{"usage":{"prompt_tokens":900,"prompt_tokens_details":{"cached_tokens":0}}}`

	got, ok := readUsage(tailOf(body))
	if !ok || got.CachedTokens != 0 || got.PromptTokens != 900 {
		t.Errorf("usage = %+v (%v), want a scored zero", got, ok)
	}
}

// A client that never asked for usage gets none, and the router must hear
// nothing rather than a zero: a run that forgot stream_options has no residency
// signal, and that has to look like no signal.
func TestAResponseWithNoUsageAtAllSaysNothing(t *testing.T) {
	if got, ok := readUsage(tailOf("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")); ok {
		t.Errorf("a response carrying no usage was scored as %+v", got)
	}
}

// A response cut off mid-block leaves an object that never closes. Half a usage
// block is not a measurement.
func TestAnUnclosedUsageBlockIsNotScored(t *testing.T) {
	if got, ok := readUsage(tailOf(`{"usage":{"prompt_tokens":900,"prompt_tokens_de`)); ok {
		t.Errorf("a truncated usage block was scored as %+v", got)
	}
}

// The tail is bounded, and the block survives at the end of a response far
// longer than the bound. Nothing here buffers the answer itself.
func TestTheTailKeepsTheEndOfALongResponseAndNotItsMiddle(t *testing.T) {
	long := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"token \"}}],\"usage\":null}\n\n", 4000)
	tail := tailOf(long, `data: {"usage":{"prompt_tokens":77,"prompt_tokens_details":{"cached_tokens":11}}}`+"\n\n")

	if len(tail) > usageTailBytes {
		t.Errorf("the tail grew to %d bytes, past its bound of %d", len(tail), usageTailBytes)
	}
	got, ok := readUsage(tail)
	if !ok || got.PromptTokens != 77 || got.CachedTokens != 11 {
		t.Errorf("usage = %+v (%v), want 77/11 off the end of a long stream", got, ok)
	}
}

// A single write larger than the bound is the ordinary case for a blocking
// answer, and the end of it is the half that matters.
func TestOneOversizedWriteIsTrimmedFromTheFront(t *testing.T) {
	body := strings.Repeat("x", 3*usageTailBytes) +
		`{"usage":{"prompt_tokens":5,"prompt_tokens_details":{"cached_tokens":5}}}`

	tail := tailOf(body)
	if len(tail) != usageTailBytes {
		t.Errorf("the tail is %d bytes, want exactly its bound of %d", len(tail), usageTailBytes)
	}
	if got, ok := readUsage(tail); !ok || got.CachedTokens != 5 {
		t.Errorf("usage = %+v (%v), want 5 cached", got, ok)
	}
}
