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
)

// ErrNoReplica means no replica could be chosen, so the request is dropped.
var ErrNoReplica = errors.New("policy: no replica available")

// Reason is why a policy chose the replica it did.
type Reason string

const (
	// ReasonRoundRobin is a stateless turn of the rotation.
	ReasonRoundRobin Reason = "ROUND_ROBIN"
)

// Request is everything a policy may know about an incoming request. The body
// is the rendered prompt the prefix index will later chunk; policies must treat
// it as read-only.
type Request struct {
	Header http.Header
	Body   []byte
}

// Choice is a policy's decision.
type Choice struct {
	Replica fleet.Replica
	Reason  Reason
}

// Policy picks the replica for a request.
type Policy interface {
	// Name identifies the policy in records and in the results table.
	Name() string
	// Choose returns the replica to dispatch to, or ErrNoReplica.
	Choose(req Request, state fleet.State) (Choice, error)
}

// ByName resolves the policy named in configuration, so that a benchmark can
// run every policy against an unchanged fleet.
func ByName(name string) (Policy, error) {
	switch name {
	case RoundRobinName:
		return NewRoundRobin(), nil
	default:
		return nil, fmt.Errorf("policy: unknown policy %q", name)
	}
}
