package bench

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
)

// Turn is one request a workload wants sent: the session it belongs to, how far
// into that session it is, and the body to POST.
type Turn struct {
	Session string
	// Index is the turn's position within its session, counted from zero.
	//
	// It is the workload's to report rather than the driver's to count. A
	// driver counts the turns one virtual user has sent, which is the same
	// number only while a user holds one conversation forever; a generator that
	// draws a fresh session every few turns makes the two diverge, and the
	// recorded index would then say a request carried a hundred turns of
	// history when it carried two. Prefix depth is what a row is read for, so
	// the number has to be the session's.
	Index int
	Body  []byte
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

// includeUsage is what every request body asks the engine for: the usage block
// carrying this request's prompt tokens and how many of them the engine answered
// out of its KV cache.
//
// Unconditional, and not a knob. It is the only per-request ground truth belief
// divergence can be measured against — vllm:request_prefill_kv_computed_tokens
// is the same quantity as a histogram and carries no request id — and a sweep
// that could be run without it would be a sweep whose rows cannot check the
// router's own prediction (#17).
//
// Two things it costs, and one it does not. It adds a trailing chunk carrying no
// content, which readStream does not count as a token, so inter-token latency is
// untouched and only the response byte count moves. It adds about forty bytes to
// every request body, which sorts last in the marshalled JSON and therefore
// leaves every prompt's leading bytes — the prefix the index chunks and the
// engine caches — exactly where they were. What it does change is the bytes, so
// the workload names below carry it: a cell recorded before this and one
// recorded after did not send the same request, and a comparison that let their
// names match would compare two workloads as one.
func includeUsage() map[string]any {
	return map[string]any{"include_usage": true}
}

// usageMarker is how a workload name records that its requests ask for usage.
const usageMarker = "usage=on"

// PressurePoint is implemented by a workload that can state the working set
// ratio it offers: the session tokens it puts in front of the fleet over the
// fleet's measured aggregate KV capacity.
//
// An optional interface rather than a method on Workload, because the fixed
// workload has no session pool and so no working set, and a method it had to
// implement would report that absence as WS 0 — a point on the axis rather than
// a workload that is not on it.
type PressurePoint interface {
	WorkingSet() float64
	// Skew is the Zipf exponent over the session pool: 0 is uniform, and
	// concentration rises from there. It travels with the working set because the
	// two are the pressure grid's axes together and neither is readable alone —
	// skew decides how much of the pool a cell of finite length actually draws
	// from, so a working set reported without it names a pressure the cell may
	// not have applied.
	Skew() float64
}

// OfferedWorkingSet is the WS point a workload offers, or zero when it does not
// state one.
//
// It asks through this function rather than at each call site because the sweep
// hands every cell a Shifted workload: a wrapper that did not forward the
// question would report every cell as stating nothing, and the divergence
// report's WS axis would be one empty column that no test failed over.
func OfferedWorkingSet(w Workload) float64 {
	if point, states := w.(PressurePoint); states {
		return point.WorkingSet()
	}
	return 0
}

// OfferedSkew is the Zipf skew a workload offers, or zero when it states none.
//
// Zero is genuinely uniform rather than an absence, which is why this does not
// distinguish the two: a workload with no session pool has no traffic to
// concentrate, and uniform is what it offers.
func OfferedSkew(w Workload) float64 {
	if point, states := w.(PressurePoint); states {
		return point.Skew()
	}
	return 0
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

// WorkingSet forwards the wrapped workload's own. Shifting moves which slice of
// the user space a cell draws from and nothing about how much pressure the pool
// represents, so hiding the figure here would lose it for every cell the sweep
// runs — which is all of them.
func (s shifted) WorkingSet() float64 { return OfferedWorkingSet(s.inner) }

// Skew forwards the wrapped workload's own, for the reason WorkingSet does:
// every cell the sweep runs is Shifted, so a wrapper that dropped it would
// report the whole pressure grid as uniform.
func (s shifted) Skew() float64 { return OfferedSkew(s.inner) }

// ConfiguredWorkingSet forwards the wrapped workload's own, for the reason
// WorkingSet and Skew are forwarded: the sweep hands every cell a shifted
// workload, and a wrapper that swallowed the question would report every cell as
// having asked for no grid point — which would put the whole grid back on one
// slice of the user space.
func (s shifted) ConfiguredWorkingSet() float64 { return ConfiguredWorkingSet(s.inner) }

// ConfiguredWorkingSet is the WS point a workload was explicitly told to offer,
// or zero when it was told none.
//
// Deliberately distinct from OfferedWorkingSet, which derives a ratio from a
// measured capacity. Only an explicit request puts a run on the grid, and only
// being on the grid moves its bytes: see GridWorkloadOffset.
func ConfiguredWorkingSet(w Workload) float64 {
	if asked, states := w.(interface{ ConfiguredWorkingSet() float64 }); states {
		return asked.ConfiguredWorkingSet()
	}
	return 0
}

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
	return fmt.Sprintf("fixed(prompt=%dB,output=%dt,seed=%d,%s)", f.cfg.PromptBytes, f.cfg.OutputTokens, f.cfg.Seed, usageMarker)
}

func (f *Fixed) Next(user, turn int) Turn {
	session := fmt.Sprintf("user-%d", user)
	body, err := json.Marshal(map[string]any{
		"model": f.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": f.filler(user, turn)},
		},
		"stream":         true,
		"stream_options": includeUsage(),
		"max_tokens":     f.cfg.OutputTokens,
	})
	if err != nil {
		// The value is a map of strings and ints built here; there is no input
		// that can make it unmarshalable.
		panic("bench: fixed workload produced an unmarshalable body: " + err.Error())
	}
	return Turn{Session: session, Index: turn, Body: body}
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
