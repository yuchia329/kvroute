// Package fleet owns the set of replicas and the load state the router knows
// about them.
//
// State is the whole of what a policy is allowed to see, so later work can add
// exact inflight counts and scraped KV utilization here without any policy or
// the router's ingress needing to change shape.
package fleet

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// Replica is one vLLM process serving one model on one GPU.
type Replica struct {
	ID      string
	BaseURL string
}

// URL builds an upstream URL on this replica, so that callers never assemble
// one out of a bare string.
func (r Replica) URL(path, rawQuery string) string {
	u := r.BaseURL + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// State is a consistent snapshot of the fleet, taken once per routing decision.
type State struct {
	Replicas []Replica
}

// Fleet is the router's view of the replicas it fronts.
type Fleet struct {
	mu       sync.RWMutex
	replicas []Replica
}

// New builds a fleet. It rejects duplicate ids and unusable base URLs, because
// a typo in a replica spec should fail at startup rather than as a routing
// error under load.
func New(replicas []Replica) (*Fleet, error) {
	if len(replicas) == 0 {
		return nil, errors.New("fleet: at least one replica is required")
	}
	seen := make(map[string]bool, len(replicas))
	for _, r := range replicas {
		if r.ID == "" {
			return nil, errors.New("fleet: replica id must not be empty")
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("fleet: duplicate replica id %q", r.ID)
		}
		seen[r.ID] = true

		u, err := url.Parse(r.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("fleet: replica %s: %w", r.ID, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("fleet: replica %s: base URL %q needs a scheme and a host", r.ID, r.BaseURL)
		}
	}
	return &Fleet{replicas: append([]Replica(nil), replicas...)}, nil
}

// State returns a snapshot for one routing decision.
func (f *Fleet) State() State {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return State{Replicas: append([]Replica(nil), f.replicas...)}
}

// ParseSpecs turns "replica-0=http://127.0.0.1:8000" command-line specs into
// replicas. A bare URL is given a positional id.
func ParseSpecs(specs []string) ([]Replica, error) {
	replicas := make([]Replica, 0, len(specs))
	for i, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		id, base, found := strings.Cut(spec, "=")
		if !found {
			id, base = fmt.Sprintf("replica-%d", i), spec
		}
		replicas = append(replicas, Replica{ID: strings.TrimSpace(id), BaseURL: strings.TrimSuffix(strings.TrimSpace(base), "/")})
	}
	return replicas, nil
}
