// Package fakereplica is a programmable stand-in for a vLLM replica.
//
// The router talks to replicas over exactly two HTTP surfaces — chat
// completions with SSE, and /metrics — so that pair is the project's single
// test seam. Everything above it (proxy, prefix index, policies, inflight
// accounting, scraping) is exercised unmodified against this fake.
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

	// Now supplies the `created` timestamp. Tests pin it so that responses are
	// byte-deterministic. Defaults to time.Now.
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

	mu       sync.Mutex
	failure  *Failure
	kvUtil   float64
	counters counters
}

type counters struct {
	requests          int64
	promptTokens      int64
	cachedTokens      int64
	prefixCacheHits   int64
	prefixCacheQuerys int64
	completionTokens  int64
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
	return &Replica{cfg: cfg, kvUtil: cfg.KVUtilization}
}

// SetFailure makes every subsequent chat completion fail with f, or clears the
// failure when f is nil.
func (r *Replica) SetFailure(f *Failure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failure = f
}

// SetKVUtilization changes the value reported as vllm:kv_cache_usage_perc.
func (r *Replica) SetKVUtilization(v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kvUtil = v
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
	id := "chatcmpl-" + hex.EncodeToString(sum[:])[:32]
	created := r.cfg.Now().Unix()

	n := r.cfg.OutputTokens
	if parsed.MaxTokens > 0 && parsed.MaxTokens < n {
		n = parsed.MaxTokens
	}
	promptTokens := estimateTokens(parsed.Messages)
	r.recordRequest(promptTokens, n)

	if parsed.Stream {
		includeUsage := parsed.StreamOptions != nil && parsed.StreamOptions.IncludeUsage
		r.streamCompletion(w, req, id, created, n, promptTokens, includeUsage)
		return
	}
	r.blockingCompletion(w, req, id, created, n, promptTokens)
}

func (r *Replica) recordRequest(promptTokens, completionTokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.requests++
	r.counters.promptTokens += int64(promptTokens)
	r.counters.completionTokens += int64(completionTokens)
	r.counters.prefixCacheQuerys += int64(promptTokens)
}

func (r *Replica) blockingCompletion(w http.ResponseWriter, req *http.Request, id string, created int64, n, promptTokens int) {
	if !r.sleep(req, r.cfg.TTFT+time.Duration(n-1)*r.cfg.InterToken) {
		return
	}
	stop := "stop"
	resp := completionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   r.cfg.Model,
		Choices: []completionChoice{{
			Index:        0,
			Message:      chatMessage{Role: "assistant", Content: generate(n)},
			FinishReason: &stop,
		}},
		Usage: &usage{
			PromptTokens:     promptTokens,
			CompletionTokens: n,
			TotalTokens:      promptTokens + n,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (r *Replica) streamCompletion(w http.ResponseWriter, req *http.Request, id string, created int64, n, promptTokens int, includeUsage bool) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	role := "assistant"
	empty := ""

	if !r.sleep(req, r.cfg.TTFT) {
		return
	}
	first := chunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: r.cfg.Model,
		Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{Role: &role, Content: &empty}}}}
	if !writeChunk(w, rc, first) {
		return
	}

	for i := range n {
		if i > 0 && !r.sleep(req, r.cfg.InterToken) {
			return
		}
		tok := token(i)
		c := chunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: r.cfg.Model,
			Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{Content: &tok}}}}
		if !writeChunk(w, rc, c) {
			return
		}
	}

	stop := "stop"
	final := chunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: r.cfg.Model,
		Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{}, FinishReason: &stop}}}
	if !writeChunk(w, rc, final) {
		return
	}

	if includeUsage {
		u := chunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: r.cfg.Model,
			Choices: []chunkChoice{},
			Usage: &usage{
				PromptTokens:     promptTokens,
				CompletionTokens: n,
				TotalTokens:      promptTokens + n,
			}}
		if !writeChunk(w, rc, u) {
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
