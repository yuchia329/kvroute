package bench

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
)

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

// Shifted returns w with its user space moved by offset, so that two
// measurements in the same run never send the same bytes.
//
// This matters more than it looks. A workload is deterministic in (user, turn)
// on purpose — a cell that is re-run has to send the same bytes, or the cache
// it is measuring is not the same cache — but that determinism makes the second
// repetition of a measurement re-send the first repetition's prompts verbatim.
// The replica still holds them, so the second repetition measures the prefix
// cache rather than prefill. Measured on the box: TTFT drops from ~325 ms to
// ~46 ms, a seven-fold difference that has nothing to do with what was being
// varied.
//
// The offset is therefore derived from the axes rather than counted, so a
// resumed measurement still re-sends its own bytes while a different one never
// re-sends somebody else's.
func Shifted(w Workload, offset int) Workload {
	if offset == 0 {
		return w
	}
	return shifted{inner: w, offset: offset}
}

type shifted struct {
	inner  Workload
	offset int
}

func (s shifted) Name() string             { return s.inner.Name() }
func (s shifted) Next(user, turn int) Turn { return s.inner.Next(s.offset+user, turn) }

// WorkloadStride separates one measurement's user space from the next. It is
// far above any concurrency the sweep reaches, so two measurements' user ranges
// cannot overlap.
const WorkloadStride = 4096

// FixedWorkload configures the fixed workload.
type FixedWorkload struct {
	// Model is what the replicas serve. There is deliberately no default: the
	// model is pinned engine configuration and ops/versions.env is its single
	// source of truth, so a constant here would be a second copy that drifts.
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
	if cfg.PromptBytes <= 0 {
		cfg.PromptBytes = 2048
	}
	if cfg.OutputTokens <= 0 {
		cfg.OutputTokens = 64
	}
	return &Fixed{cfg: cfg}
}

// Name carries the seed as well as the shape, because the seed decides the bytes.
// Two policies compared over one load point have to have sent the same prompts,
// and the comparison checks that by this name: a name that named only the shape
// would let two runs seeded differently pass as the same workload.
func (f *Fixed) Name() string {
	return fmt.Sprintf("fixed(prompt=%dB,output=%dt,seed=%d)", f.cfg.PromptBytes, f.cfg.OutputTokens, f.cfg.Seed)
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
