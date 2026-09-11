package residency

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// TokenizePath is the engine's tokenizer endpoint.
const TokenizePath = "/tokenize"

// DefaultTokenizeTimeout is how long a tokenization may take before the
// request is routed without one.
//
// Chosen, not measured. Tokenizing is on the request path — the request waits
// for it before it is routed at all — so its budget is a slice of the TTFT the
// SLO allows (990 ms, ADR-0003), and an answer that arrives after half of that
// has gone cannot buy a decision worth what it cost. Whether it ever bound is on
// every row: a request that was not tokenized in time is routed under its own
// reason, so the decision mix counts them.
const DefaultTokenizeTimeout = 500 * time.Millisecond

// EngineTokenizer asks a replica's engine for the token ids it would compute
// for a chat request.
//
// The router has no tokenizer and does not grow one (idea.md §4.3). The prefix
// index gets away without one because it only has to agree with itself; exact
// residency cannot, because the engine's events name blocks of tokens. So the
// engine is asked: it renders the chat template and tokenizes with the very
// code a chat completion of the same body runs, so the tokens cannot drift from
// the ones the engine will cache. The price is one request to the engine before
// routing, and it is paid in router overhead where the rows can show it.
type EngineTokenizer struct {
	// Client sends the requests. Nil is http.DefaultClient.
	Client *http.Client
	// Timeout bounds one tokenization. Zero is DefaultTokenizeTimeout.
	Timeout time.Duration
}

// Tokenize returns the token ids the replica's engine computes for this chat
// request body.
//
// The body goes verbatim. The endpoint reads every field that shapes the
// rendered prompt — the messages, tools, the template's own arguments — and
// ignores the rest, so sending the body unchanged is what guarantees the prompt
// it tokenizes is the prompt the chat completion will run.
func (t *EngineTokenizer) Tokenize(ctx context.Context, replica fleet.Replica, body []byte) ([]uint32, error) {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(t.Timeout, DefaultTokenizeTimeout))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, replica.URL(TokenizePath, ""), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("residency: tokenize on %s: %w", replica.ID, err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("residency: tokenize on %s: %w", replica.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("residency: tokenize on %s: status %d: %s", replica.ID, resp.StatusCode, bytes.TrimSpace(said))
	}
	var answer struct {
		Tokens []uint32 `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, fmt.Errorf("residency: tokenize on %s: %w", replica.ID, err)
	}
	return answer.Tokens, nil
}
