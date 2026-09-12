package router_test

import (
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// The fleet a decision was made against is on every row, under a policy that
// consults none of it.
//
// Under every policy for the reason the honoured rate is under every policy: the
// run that cuts a grid has to see what the rule would have compared against, and
// a column that appeared only where something read it could not be used to say
// what a threshold would have done elsewhere.
func TestEveryRowCarriesTheFleetTheDecisionWasMadeAgainst(t *testing.T) {
	_, first := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	_, second := startFake(t, fakereplica.Config{ID: "replica-1", OutputTokens: 2})
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+first, "replica-1="+second)

	post(t, r.url, blockingRequest)
	rows := r.rows.wait(t, 1)

	if !rows[0].FleetLoadRead {
		t.Fatal("the row does not carry the fleet's load, so nothing can be said about what the decision was made against")
	}
	if rows[0].FleetMinInflight != 0 || rows[0].FleetMeanInflight != 0 {
		t.Errorf("minimum %d, mean %v on an idle fleet, want 0 and 0", rows[0].FleetMinInflight, rows[0].FleetMeanInflight)
	}
}

// An idle fleet and an unrecorded one are different states, and only the flag
// tells them apart: a run whose rows predate the column would otherwise read as
// a fleet that was idle for every decision it ever made — which is the shape a
// grid would then be cut to chase.
func TestAnUnrecordedFleetIsNotAnIdleOne(t *testing.T) {
	var row record.Request

	if row.FleetLoadRead {
		t.Error("a row nobody wrote the fleet's load onto claims to carry it")
	}
}

// What the columns are for: the two figures the load condition can be compared
// against, as the decision saw them, so a report can say which of them the rule
// was really testing (#31).
func TestTheRecordedFleetLoadIsTheOneTheDecisionSaw(t *testing.T) {
	// Slow enough that requests sent while it is answering are still in flight
	// when the next decision is made, and every request goes to this one replica
	// so the fleet's two figures differ.
	_, busy := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2, TTFT: 500 * time.Millisecond})
	_, idle := startFake(t, fakereplica.Config{ID: "replica-1", OutputTokens: 2})
	r := startRouterWith(t, pinned{to: "replica-0"}, "replica-0="+busy, "replica-1="+idle)

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(r.url, blockingRequest) //nolint:errcheck // the row is what this test reads, not the response
		}()
	}
	// Held until the router has all three on the fleet, so the fourth decision
	// is made against a known fleet rather than against whatever the scheduler
	// happened to leave in flight.
	waitForInflight(t, r, 3)

	post(t, r.url, blockingRequest)
	wg.Wait()

	last := lastRow(t, r.rows.parse(t))
	if !last.FleetLoadRead {
		t.Fatal("the row does not carry the fleet's load")
	}
	if last.FleetMinInflight != 0 {
		t.Errorf("minimum inflight = %d, want 0: replica-1 was handed nothing", last.FleetMinInflight)
	}
	if want := 1.5; last.FleetMeanInflight != want {
		t.Errorf("mean inflight = %v, want %v: three requests over two replicas", last.FleetMeanInflight, want)
	}
}

// pinned sends everything to one replica, so that a test can build a fleet
// whose minimum and mean are known and different.
type pinned struct{ to string }

func (pinned) Name() string { return "pinned" }

func (p pinned) Choose(_ policy.Request, state fleet.State) (policy.Choice, error) {
	for _, c := range state.Replicas {
		if c.ID == p.to {
			return policy.Choice{Replica: c.Replica, Reason: policy.ReasonLeastOutstanding, Inflight: c.Inflight}, nil
		}
	}
	return policy.Choice{}, policy.ErrNoReplica
}

// waitForInflight blocks until the router has n requests on the fleet.
func waitForInflight(t *testing.T, r routed, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.inflight(t) < n {
		if time.Now().After(deadline) {
			t.Fatalf("inflight reached %d, want %d", r.inflight(t), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// lastRow is the row of the request that was sent last, which is the one whose
// decision saw the fleet the test built.
func lastRow(t *testing.T, rows []record.Request) record.Request {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("no rows were written")
	}
	last := rows[0]
	for _, row := range rows[1:] {
		if row.StartedAt.After(last.StartedAt) {
			last = row
		}
	}
	return last
}
