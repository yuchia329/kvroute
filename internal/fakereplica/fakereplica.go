// Package fakereplica is a programmable stand-in for a vLLM replica.
//
// The router talks to replicas over exactly two HTTP surfaces — chat
// completions with SSE, and /metrics — so that pair is the project's single
// test seam. Everything above it (the router's ingress, the prefix index, the
// policies, inflight accounting and scraping) is exercised unmodified against
// this fake.
//
// The fake is only worth having if it stays honest, so the assertions in
// test/contract run against both this and a live replica of the pinned engine
// version.
package fakereplica

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultModel = "hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4"

// Config programs one fake replica. The zero value is usable; every field has a
// working default.
type Config struct {
	// ID names the replica in logs. Defaults to "fake".
	ID string
	// Model is echoed back in responses. Defaults to the project's model.
	Model string

	// TTFT is how long the replica waits before emitting its first token.
	TTFT time.Duration
	// InterToken is the delay between subsequent tokens.
	InterToken time.Duration
	// OutputTokens is how many tokens to generate when the request does not cap
	// it with max_tokens. Defaults to 8.
	OutputTokens int

	// KVUtilization is reported as vllm:kv_cache_usage_perc.
	KVUtilization float64

	// Now supplies the `created` timestamp. Defaults to time.Now.
	//
	// Tests pin it because `created` is in whole seconds: two requests that
	// straddle a second boundary would otherwise produce responses differing by
	// one, and the byte-identity test compares a stream taken directly against
	// one taken through the router. This is a knob on the fake, which is
	// required to be programmable — the router's own timing code stays real.
	Now func() time.Time
}

// Failure makes the replica reject chat completions with an upstream error, so
// that the router can be tested for surfacing it faithfully rather than masking
// it as its own.
type Failure struct {
	Status  int
	Message string
	Type    string
}

// Replica is a running fake. Use Handler to mount it.
type Replica struct {
	cfg Config

	mu         sync.Mutex
	failure    *Failure
	counters   counters
	histograms map[string]*histogram
}

type counters struct {
	requests         int64
	promptTokens     int64
	completionTokens int64
}

// New builds a fake replica from cfg, filling in defaults.
func New(cfg Config) *Replica {
	if cfg.ID == "" {
		cfg.ID = "fake"
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.OutputTokens <= 0 {
		cfg.OutputTokens = 8
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Replica{cfg: cfg, histograms: newHistograms()}
}

// SetFailure makes every subsequent chat completion fail with f, or clears the
// failure when f is nil.
func (r *Replica) SetFailure(f *Failure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failure = f
}

// Handler returns the replica's HTTP surface.
func (r *Replica) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", r.handleChatCompletions)
	mux.HandleFunc("GET /metrics", r.handleMetrics)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

type chatRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	Stream        bool          `json:"stream"`
	MaxTokens     int           `json:"max_tokens"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// completion is one response the fake is about to produce. Streaming and
// blocking share it so that the two paths cannot drift apart in identity,
// length or usage accounting.
type completion struct {
	id           string
	created      int64
	model        string
	tokens       int
	promptTokens int
	includeUsage bool
}

func (c completion) chunk(choices []chunkChoice, u *usage) chunk {
	return chunk{
		ID:      c.id,
		Object:  "chat.completion.chunk",
		Created: c.created,
		Model:   c.model,
		Choices: choices,
		Usage:   u,
	}
}

func (c completion) usage() *usage {
	return &usage{
		PromptTokens:     c.promptTokens,
		CompletionTokens: c.tokens,
		TotalTokens:      c.promptTokens + c.tokens,
	}
}

func (r *Replica) handleChatCompletions(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequestError", "could not read request body")
		return
	}

	var parsed chatRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequestError", "invalid JSON body")
		return
	}
	if len(parsed.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "BadRequestError", "messages is required and must be non-empty")
		return
	}

	r.mu.Lock()
	failure := r.failure
	r.mu.Unlock()
	if failure != nil {
		writeError(w, failure.Status, failure.Type, failure.Message)
		return
	}

	// The completion id is derived from the request rather than randomised, so
	// that the same request produces byte-identical responses whether it was
	// sent directly or through the router.
	sum := sha256.Sum256(body)
	tokens := r.cfg.OutputTokens
	if parsed.MaxTokens > 0 && parsed.MaxTokens < tokens {
		tokens = parsed.MaxTokens
	}
	c := completion{
		id:           "chatcmpl-" + hex.EncodeToString(sum[:])[:32],
		created:      r.cfg.Now().Unix(),
		model:        r.cfg.Model,
		tokens:       tokens,
		promptTokens: estimateTokens(parsed.Messages),
		includeUsage: parsed.StreamOptions != nil && parsed.StreamOptions.IncludeUsage,
	}
	r.observe(c)

	if parsed.Stream {
		r.streamCompletion(w, req, c)
		return
	}
	r.blockingCompletion(w, req, c)
}

// observe folds one completion into the counters and histograms /metrics
// reports. The fake's latency model is deterministic, so the values it records
// are the ones it is about to spend.
func (r *Replica) observe(c completion) {
	ttft := r.cfg.TTFT.Seconds()
	itl := r.cfg.InterToken.Seconds()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.requests++
	r.counters.promptTokens += int64(c.promptTokens)
	r.counters.completionTokens += int64(c.tokens)

	r.histograms["vllm:time_to_first_token_seconds"].observe(ttft)
	r.histograms["vllm:e2e_request_latency_seconds"].observe(ttft + float64(c.tokens-1)*itl)
	r.histograms["vllm:request_prefill_kv_computed_tokens"].observe(float64(c.promptTokens))
	for range c.tokens - 1 {
		r.histograms["vllm:inter_token_latency_seconds"].observe(itl)
	}
}

func (r *Replica) blockingCompletion(w http.ResponseWriter, req *http.Request, c completion) {
	if !r.sleep(req, r.cfg.TTFT+time.Duration(c.tokens-1)*r.cfg.InterToken) {
		return
	}
	stop := "stop"
	resp := completionResponse{
		ID:      c.id,
		Object:  "chat.completion",
		Created: c.created,
		Model:   c.model,
		Choices: []completionChoice{{
			Index:        0,
			Message:      chatMessage{Role: "assistant", Content: generate(c.tokens)},
			FinishReason: &stop,
		}},
		Usage: c.usage(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (r *Replica) streamCompletion(w http.ResponseWriter, req *http.Request, c completion) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	role, empty := "assistant", ""

	if !r.sleep(req, r.cfg.TTFT) {
		return
	}
	opening := c.chunk([]chunkChoice{{Index: 0, Delta: chunkDelta{Role: &role, Content: &empty}}}, nil)
	if !writeChunk(w, rc, opening) {
		return
	}

	for i := range c.tokens {
		if i > 0 && !r.sleep(req, r.cfg.InterToken) {
			return
		}
		tok := token(i)
		if !writeChunk(w, rc, c.chunk([]chunkChoice{{Index: 0, Delta: chunkDelta{Content: &tok}}}, nil)) {
			return
		}
	}

	stop := "stop"
	final := c.chunk([]chunkChoice{{Index: 0, Delta: chunkDelta{}, FinishReason: &stop}}, nil)
	if !writeChunk(w, rc, final) {
		return
	}

	if c.includeUsage {
		if !writeChunk(w, rc, c.chunk([]chunkChoice{}, c.usage())) {
			return
		}
	}

	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	_ = rc.Flush()
}

// sleep waits for d, reporting false if the client went away first.
func (r *Replica) sleep(req *http.Request, d time.Duration) bool {
	if d <= 0 {
		return req.Context().Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-req.Context().Done():
		return false
	}
}

func writeChunk(w http.ResponseWriter, rc *http.ResponseController, c chunk) bool {
	payload, err := json.Marshal(c)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return false
	}
	return rc.Flush() == nil
}

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role    *string `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *usage             `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason *string     `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func writeError(w http.ResponseWriter, status int, typ, message string) {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if typ == "" {
		typ = "InternalServerError"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object":  "error",
		"message": message,
		"type":    typ,
		"param":   nil,
		"code":    status,
	})
}

// token is the i-th generated token. Deterministic, because a byte-identity
// test cannot compare two streams of random text.
func token(i int) string {
	return fmt.Sprintf(" t%d", i)
}

func generate(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(token(i))
	}
	return b.String()
}

// estimateTokens approximates a tokenizer at roughly four bytes per token. The
// fake reports token counts; it does not need to agree with a real tokenizer to
// exercise the router.
func estimateTokens(messages []chatMessage) int {
	bytes := 0
	for _, m := range messages {
		bytes += len(m.Role) + len(m.Content)
	}
	return max(bytes/4, 1)
}
