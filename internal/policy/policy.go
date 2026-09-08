// Package policy holds the routing policies behind one interface.
//
// A policy maps a request plus fleet state to a replica and a reason. The
// reason is a return value rather than a log side effect, because the decision
// mix — how often affinity was taken, declined or unavailable — is a reported
// result of the experiment.
package policy

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/session"
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
	// ReasonCold is a request no replica was believed to hold anything for,
	// placed on load because there was no cache locality to preserve.
	//
	// It is its own reason rather than folded into least-outstanding, because
	// the decision mix is a reported result and the two say different things: a
	// cold request is one the index had nothing for, and a policy that produced
	// nothing but cold decisions has an index that is not working, which no
	// goodput figure beside it would reveal.
	ReasonCold Reason = "COLD"
)

// Order is the order the policies are compared in: the naive baseline first, then
// each policy that claims to improve on it, as idea.md §5 numbers them.
//
// It is here rather than in the harness because the comparison table has to put
// the baseline in the same column whichever order the runs happened in, and the
// policies are what the ordering is about.
var Order = []string{
	RoundRobinName,
	LeastOutstandingName,
	SessionAffinityName,
	PrefixAffinityName,
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
	case PrefixAffinityName:
		if opts.PrefixIndex == nil {
			return nil, fmt.Errorf("policy: %s needs a prefix index, and its bounds are measurements rather than defaults: build one with prefix.Calibration", PrefixAffinityName)
		}
		return NewPrefixAffinity(opts.PrefixIndex), nil
	default:
		return nil, fmt.Errorf("policy: unknown policy %q", name)
	}
}
