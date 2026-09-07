package bench

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
)

// DefaultModel is the model every replica serves.
const DefaultModel = "hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4"

// Turn is one request a workload wants sent: the session it belongs to and the
// body to POST.
type Turn struct {
	Session string
	Body    []byte
}

// Workload produces the turns a driver sends.
//
// It is an interface so the multi-turn generator can replace the fixed workload
// below without the driver changing shape. The driver's job is to hold
// concurrency and account for outcomes; what it sends is somebody else's.
type Workload interface {
	// Name identifies the workload in the cell record.
	Name() string
	// Next builds the turn a virtual user should send next. It must be
	// deterministic in its arguments: a cell that is re-run has to send the
	// same bytes, or the cache it is measuring is not the same cache.
	Next(user, turn int) Turn
}

// FixedWorkload configures the fixed workload.
type FixedWorkload struct {
	Model string
	// PromptBytes is roughly how large each user message is. Bytes rather than
	// tokens because the harness does not run a tokenizer.
	PromptBytes int
	// OutputTokens caps generation, so a cell's duration is dominated by the
	// fleet rather than by how long a model wants to talk.
	OutputTokens int
	// Seed makes the filler reproducible across runs of the same cell.
	Seed uint64
}

// Fixed is a workload of independent single-turn requests, all the same shape
// and none sharing a prefix with another.
//
// It exists to put load on the fleet, not to exercise cache locality: with no
// shared prefixes there is nothing for prefix-aware routing to be aware of, so
// the only policy it can honestly evaluate is the round-robin baseline whose
// concurrency scaling this sweep measures. The multi-turn generator with shared
// system prompts, growing histories and Zipf-skewed session reuse is what turns
// this axis into a policy comparison, and it replaces this type rather than
// extending it.
type Fixed struct {
	cfg FixedWorkload
}

// NewFixedWorkload builds the fixed workload, filling in defaults.
func NewFixedWorkload(cfg FixedWorkload) *Fixed {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.PromptBytes <= 0 {
		cfg.PromptBytes = 2048
	}
	if cfg.OutputTokens <= 0 {
		cfg.OutputTokens = 64
	}
	return &Fixed{cfg: cfg}
}

func (f *Fixed) Name() string {
	return fmt.Sprintf("fixed(prompt=%dB,output=%dt)", f.cfg.PromptBytes, f.cfg.OutputTokens)
}

func (f *Fixed) Next(user, turn int) Turn {
	session := fmt.Sprintf("user-%d", user)
	body, err := json.Marshal(map[string]any{
		"model": f.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": f.filler(user, turn)},
		},
		"stream":     true,
		"max_tokens": f.cfg.OutputTokens,
	})
	if err != nil {
		// The value is a map of strings and ints built here; there is no input
		// that can make it unmarshalable.
		panic("bench: fixed workload produced an unmarshalable body: " + err.Error())
	}
	return Turn{Session: session, Body: body}
}

// filler builds a prompt of roughly the configured size, seeded on the virtual
// user and turn so a re-run of a cell sends the same bytes and no two turns
// share a prefix by accident.
func (f *Fixed) filler(user, turn int) string {
	rng := rand.New(rand.NewPCG(f.cfg.Seed+uint64(user), uint64(turn)))
	var b strings.Builder
	b.Grow(f.cfg.PromptBytes + 16)
	for b.Len() < f.cfg.PromptBytes {
		fmt.Fprintf(&b, "%08x ", rng.Uint32())
	}
	return b.String()
}
