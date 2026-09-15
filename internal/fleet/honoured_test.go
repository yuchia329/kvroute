package fleet_test

import (
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/belief"
	"github.com/yuchia329/kvroute/internal/fleet"
)

// honouredOf pulls one replica's rate out of a snapshot.
func honouredOf(t *testing.T, f *fleet.Fleet, id string) belief.Honoured {
	t.Helper()
	c, present := f.State().Candidate(id)
	if !present {
		t.Fatalf("no replica %s in the snapshot", id)
	}
	return c.Honoured
}

func oneReplica(t *testing.T, opts ...fleet.Option) *fleet.Fleet {
	t.Helper()
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: "http://127.0.0.1:8000"}}, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// A replica nobody has asked anything of has no rate, which is the case the
// spill rule's graceful degradation is built on: it declines nothing.
func TestAReplicaNobodyHasSentAScoringRequestToHasNoHonouredRate(t *testing.T) {
	f := oneReplica(t)

	if got := honouredOf(t, f, "replica-0"); got.Read {
		t.Errorf("a replica nobody asked read %v, want unread", got)
	}
}

// What the router fed back reaches the policy's snapshot, which is the whole
// path the signal travels: response usage in, routing decision out.
func TestWhatTheEnginesHonouredReachesThePolicysSnapshot(t *testing.T) {
	f := oneReplica(t, fleet.WithHonouredWindow(belief.Window{Requests: 8, TTL: time.Minute, Quorum: 2}))

	now := time.Now()
	if err := f.ObserveHonoured("replica-0", now, 400, 100); err != nil {
		t.Fatalf("ObserveHonoured: %v", err)
	}
	if got := honouredOf(t, f, "replica-0"); got.Read {
		t.Fatalf("one observation under a quorum of two read %v", got)
	}
	if err := f.ObserveHonoured("replica-0", now, 400, 300); err != nil {
		t.Fatalf("ObserveHonoured: %v", err)
	}

	got := honouredOf(t, f, "replica-0")
	if !got.Read || got.Fraction != 0.5 {
		t.Errorf("snapshot carries %v, want a read 0.5: 400 of 800 claimed tokens were held", got)
	}
}

// A replica the fleet does not front is a mis-wired router, not a routing
// problem, so it is an error rather than a silently discarded observation.
func TestAnObservationForAReplicaTheFleetDoesNotHaveIsRefused(t *testing.T) {
	f := oneReplica(t)

	if err := f.ObserveHonoured("replica-9", time.Now(), 100, 100); err == nil {
		t.Error("an observation for a replica the fleet does not front was accepted")
	}
}

// A window that could never produce a reading would disable the condition for
// the length of a run without anything saying so, which is the failure #28 is
// about. It is refused where every other unusable configuration is: at startup.
func TestAFleetRefusesAWindowThatCouldNeverProduceAReading(t *testing.T) {
	_, err := fleet.New(
		[]fleet.Replica{{ID: "replica-0", BaseURL: "http://127.0.0.1:8000"}},
		fleet.WithHonouredWindow(belief.Window{Requests: 4, TTL: time.Minute, Quorum: 16}),
	)
	if err == nil {
		t.Error("a quorum larger than the window was accepted")
	}
}
