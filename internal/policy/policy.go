// Package policy holds the routing policies behind one interface.
//
// A policy maps a request plus fleet state to a replica and a reason. The
// reason is a return value rather than a log side effect, because the decision
// mix — how often affinity was taken, declined or unavailable — is a reported
// result of the experiment.
package policy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/residency"
	"github.com/yuchia329/kvroute/internal/session"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// ErrNoReplica means no replica could be chosen, so the request is dropped.
var ErrNoReplica = errors.New("policy: no replica available")

// Reason is why a policy chose the replica it did.
type Reason string

const (
	// ReasonRoundRobin is a stateless turn of the rotation.
	ReasonRoundRobin Reason = "ROUND_ROBIN"
	// ReasonLeastOutstanding is the replica with the fewest inflight requests.
	ReasonLeastOutstanding Reason = "LEAST_OUTSTANDING"
	// ReasonSessionAffinity is the replica the request's session hashes to.
	ReasonSessionAffinity Reason = "SESSION_AFFINITY"
	// ReasonSessionUnidentified is a request no session could be identified for,
	// rotated because there was nothing to hash.
	//
	// It is its own reason rather than folded into session affinity because the
	// two are different decisions: a pile of unidentified requests on one replica
	// looks exactly like one hot session, and the decision mix is a reported
	// result.
	ReasonSessionUnidentified Reason = "SESSION_UNIDENTIFIED"
	// ReasonPrefixAffinity is the replica believed to hold the longest leading
	// run of this prompt's blocks.
	ReasonPrefixAffinity Reason = "PREFIX_AFFINITY"
	// ReasonPrefixHash is the replica the stateless hash of this prompt's leading
	// blocks ranked first, kept because load did not argue against it.
	ReasonPrefixHash Reason = "PREFIX_HASH"
	// ReasonHashDeflected is a request the load term moved off the replica the
	// hash ranked first.
	//
	// Its own reason because the balance between the two terms is the policy: the
	// weight is swept, and a grid point that deflected nothing and one that
	// deflected everything would otherwise be told apart only by their goodput,
	// which is the number the sweep is trying to explain rather than a way of
	// explaining it.
	ReasonHashDeflected Reason = "HASH_DEFLECTED"
	// ReasonPromptUnhashed is a request whose prompt was shorter than the hash's
	// window, placed on load because it had no leading blocks to hash.
	//
	// Its own reason for the reason PROMPT_UNTOKENIZED is: a run whose prompts
	// never filled the window routed on load throughout, and folded into the hash
	// decisions that would read as a hash that preferred nothing.
	ReasonPromptUnhashed Reason = "PROMPT_UNHASHED"
	// ReasonCold is a request no replica was believed to hold anything for,
	// placed on load because there was no cache locality to preserve.
	//
	// It is its own reason rather than folded into least-outstanding, because
	// the decision mix is a reported result and the two say different things: a
	// cold request is one the index had nothing for, and a policy that produced
	// nothing but cold decisions has an index that is not working, which no
	// goodput figure beside it would reveal.
	ReasonCold Reason = "COLD"
	// ReasonSpillKV is an affinity declined because the best match's KV cache
	// was over its high-water mark, and the request placed on load instead.
	ReasonSpillKV Reason = "SPILL_KV"
	// ReasonSpillLoad is an affinity declined because the best match was buried
	// under inflight relative to the fleet, and the request placed on load
	// instead.
	//
	// Two reasons rather than one SPILL, because the two conditions are
	// physically different pressures and the pressure grid crosses two axes to
	// fire them separately. A single reason would leave a grid cell unable to
	// say whether it spilled because the fleet was out of cache or because the
	// traffic was skewed, which is the one thing that grid exists to answer.
	ReasonSpillLoad Reason = "SPILL_LOAD"
	// ReasonPromptUntokenized is a request whose prompt the engine could not
	// tokenize in time, placed on load because there was nothing to match it
	// with.
	//
	// Its own reason rather than folded into COLD, for the reason
	// SESSION_UNIDENTIFIED is its own: cold says no engine held the prompt, and
	// this says nobody could look. A run whose tokenizer was failing would
	// otherwise read as an exact index that found nothing.
	ReasonPromptUntokenized Reason = "PROMPT_UNTOKENIZED"
)

// Spilled reports whether this reason is a declined affinity, so that callers
// counting the decision mix do not each carry their own list of which reasons
// are spills.
func (r Reason) Spilled() bool { return r == ReasonSpillKV || r == ReasonSpillLoad }

// Order is the order the policies are compared in: the naive baseline first, then
// each policy that claims to improve on it, as idea.md §5 numbers them. Exact
// residency comes last, beside the policy it is the exact counterpart of.
//
// It is here rather than in the harness because the comparison table has to put
// the baseline in the same column whichever order the runs happened in, and the
// policies are what the ordering is about.
//
// The stateless prefix hash is the one entry idea.md §5 does not number. It sits
// immediately before prefix affinity so that the three cache-aware policies read
// as the ladder they are — none, then believed, then exact residency knowledge —
// and §5's five keep their own order among themselves. Listed rather than left
// to sort after them: the table's trailing group is for names no policy has, so
// that a mistyped -policy shows up instead of disappearing, and a real policy
// landing there would be indistinguishable from that typo.
var Order = []string{
	RoundRobinName,
	LeastOutstandingName,
	SessionAffinityName,
	PrefixHashName,
	PrefixAffinityName,
	ExactResidencyName,
}

// Request is everything a policy may know about an incoming request. The body
// is the rendered prompt the prefix index will later chunk; policies must treat
// it as read-only.
type Request struct {
	Header http.Header
	Body   []byte
	// Session is the conversation this request belongs to, resolved by the
	// router before any policy sees it.
	//
	// Resolved once at ingress rather than by each policy that wants it, because
	// the router writes the same identity onto the request's row: a policy that
	// derived its own could route on a session the record does not mention, and
	// no later analysis could reconstruct why a request went where it did. It is
	// zero when the request identified no conversation, which a policy that
	// routes on it has to handle rather than treat as a session named "".
	Session session.Session
	// Context is the request's own, so that work a policy does on its behalf —
	// asking an engine to tokenize it — ends when the client goes away. Nil is
	// context.Background().
	Context context.Context
}

// Choice is a policy's decision.
type Choice struct {
	Replica fleet.Replica
	Reason  Reason
	// PrefixMatchBytes is how much of this prompt's leading bytes the chosen
	// replica was believed to already hold. Zero when the policy did not consult
	// a prefix index, and zero when it did and found nothing.
	//
	// In bytes, because the index has no tokenizer and will not pretend to one:
	// it chunks the prompt into its own fixed byte blocks, so this is a length
	// of prompt and not a count of tokens. The measured prompt bytes-per-token
	// ratio published beside it is what converts it, and keeping the conversion
	// outside the figure is what stops a router-side belief from being reported
	// in the engine's units.
	//
	// It travels on the decision because it is the router's prediction of the
	// prefix cache hit the engine will record, and the gap between the two is a
	// measured result of this project. A prediction reconstructed after the fact
	// from an index that has moved on would not be the one the decision was made
	// on.
	PrefixMatchBytes int
	// Inflight is what the chosen replica's inflight was when the decision was
	// made, not counting this request.
	//
	// It travels on the decision rather than being read off the fleet afterwards,
	// because what the policy weighed and what the fleet looks like a moment
	// later are different figures, and the row wants the one the decision was
	// made on. Without it, a table showing that one policy balanced better than
	// another would rest on nothing the rows can show.
	Inflight int
	// KV is the chosen replica's scraped KV utilization when the decision was
	// made, or an unread reading when no scrape had answered for it.
	//
	// It travels on the decision for the same reason Inflight does, and it is
	// the pressure the spill rule was weighed against: a grid table claiming one
	// high-water mark spilled more than another would otherwise rest on nothing
	// the rows can show. Unread is kept distinct from zero here as everywhere,
	// because a run whose scrapes were failing routed with the KV condition
	// silently disabled, and a column of zeros would look like a fleet with
	// empty caches instead.
	KV vllmmetrics.KVUtilization
	// DeclinedMatchBytes is the prefix match the spill rule gave up, in bytes.
	// Zero unless this decision was a spill.
	//
	// It is the cost side of the tradeoff the thresholds are swept to find.
	// PrefixMatchBytes says what the chosen replica was believed to hold, which
	// on a spill is usually nothing; this says what was on the table when the
	// rule declined. Without it a grid point can say how often it spilled but
	// not what its spills were worth, and those are the two halves of choosing a
	// threshold.
	DeclinedMatchBytes int
	// DeclinedKV and DeclinedInflight are the pressure on the replica the spill
	// rule turned down. Zero and unread on every decision that declined nothing.
	//
	// They are the figures the rule actually fired on, and without them a run
	// cannot explain its own spills. KV and Inflight above describe the replica
	// that was chosen, which on a spill is by construction a replica *under* the
	// threshold — so a column of them has the declining figure missing exactly
	// where it matters. The run of 2026-09-10 hit this: its KV column peaked at
	// 0.697 while a high-water mark of 0.70 was demonstrably declining matches,
	// because every row reported the target rather than the replica turned down.
	DeclinedKV       vllmmetrics.KVUtilization
	DeclinedInflight int
	// PrefixMatchTokens and DeclinedMatchTokens are PrefixMatchBytes and
	// DeclinedMatchBytes for the policy that knows what a replica holds in the
	// engine's own tokens rather than in bytes of prompt: exact residency, whose
	// index the engines' events feed. Zero under every other policy, and the byte
	// figures are zero under that one. A decision carries its prediction in the
	// unit it was made in, never converted into the other's.
	PrefixMatchTokens   int
	DeclinedMatchTokens int
	// Tokenize is how long the engine took to tokenize the prompt before the
	// decision could be made, under the one policy that has to ask. It is inside
	// the router overhead the row reports, and carried separately so that the
	// cost of knowing exactly can be told apart from the cost of deciding.
	Tokenize time.Duration
}

// Tuned is implemented by a policy carrying tunables the record has to name.
//
// The router reports them on /router/stats, and the harness refuses a sweep
// whose cells would be labelled with a grid point the router is not running.
// That check is the same one that guards the policy name, and for the same
// reason: the router is started with its configuration and the sweep is only
// told what that was, so nothing else stands between a mistyped threshold and a
// grid of cells labelled with a point that never ran.
type Tuned interface {
	Tunables() Spill
}

// Policy picks the replica for a request.
type Policy interface {
	// Name identifies the policy in records and in the results table.
	Name() string
	// Choose returns the replica to dispatch to, or ErrNoReplica.
	Choose(req Request, state fleet.State) (Choice, error)
}

// Options is what a policy needs beyond its name.
//
// It exists because exactly one policy has a dependency it cannot build for
// itself, and that dependency is a calibrated one: an index sized and expired
// against figures measured off the fleet. Letting ByName default it would put a
// guess at the centre of the policy the project's claim rests on, so the
// dependency is passed in and its absence is an error.
type Options struct {
	// PrefixIndex is the index prefix affinity routes on. Required by that
	// policy and ignored by the others, which route on the fleet snapshot alone.
	PrefixIndex *prefix.Index
	// Hash is the window and weighting the stateless prefix hash routes at.
	// Required by that policy, which will not invent either, and ignored by the
	// others. See Hash: neither figure is a measurement, and neither is
	// defaulted.
	Hash Hash
	// Spill is the pressure at which prefix affinity is declined. Its zero value
	// is no spill rule, which is the policy measured before one existed, so this
	// is optional where PrefixIndex is required: an unset threshold is a
	// meaningful configuration and an unset index is not. Exact residency runs
	// the same rule at the same thresholds.
	Spill Spill
	// ResidencyIndex and Tokenizer are what exact residency routes on: the
	// engines' own account of their caches, and a way to put a prompt in the
	// tokens that account is kept in. Both are required by that policy and
	// ignored by the others.
	ResidencyIndex *residency.Index
	Tokenizer      Tokenizer
}

// ByName resolves the policy named in configuration, so that a benchmark can
// run every policy against an unchanged fleet.
func ByName(name string, opts Options) (Policy, error) {
	switch name {
	case RoundRobinName:
		return NewRoundRobin(), nil
	case LeastOutstandingName:
		return NewLeastOutstanding(), nil
	case SessionAffinityName:
		return NewSessionAffinity(), nil
	case PrefixHashName:
		if err := opts.Hash.Validate(); err != nil {
			return nil, err
		}
		if !opts.Hash.Stated() {
			return nil, fmt.Errorf("policy: %s needs a hash window: how many leading blocks it covers is a number nobody has published, so it is stated for every run rather than defaulted", PrefixHashName)
		}
		return NewPrefixHash(opts.Hash), nil
	case PrefixAffinityName:
		if opts.PrefixIndex == nil {
			return nil, fmt.Errorf("policy: %s needs a prefix index, and its bounds are measurements rather than defaults: build one with prefix.Calibration", PrefixAffinityName)
		}
		if err := opts.Spill.Validate(); err != nil {
			return nil, err
		}
		return NewPrefixAffinity(opts.PrefixIndex, opts.Spill), nil
	case ExactResidencyName:
		if opts.ResidencyIndex == nil {
			return nil, fmt.Errorf("policy: %s needs a residency index fed by the engines' KV cache events", ExactResidencyName)
		}
		if opts.Tokenizer == nil {
			return nil, fmt.Errorf("policy: %s needs a tokenizer: the engines name their blocks by tokens and the router has none of its own", ExactResidencyName)
		}
		if err := opts.Spill.Validate(); err != nil {
			return nil, err
		}
		return NewExactResidency(opts.ResidencyIndex, opts.Tokenizer, opts.Spill), nil
	default:
		return nil, fmt.Errorf("policy: unknown policy %q", name)
	}
}
