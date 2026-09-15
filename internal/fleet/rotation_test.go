package fleet_test

import (
	"errors"
	"testing"

	"github.com/yuchia329/kvroute/internal/fleet"
)

// member finds one replica in the fleet's full roster, in rotation or not.
func member(t *testing.T, f *fleet.Fleet, id string) fleet.Member {
	t.Helper()
	for _, m := range f.Members() {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("the fleet has no member %s", id)
	return fleet.Member{}
}

// A drain is the graceful half of taking a replica away: it gets no new work, and
// the work it already has is left alone to finish. Both halves are checked,
// because either one alone is a different operation — a replica that took no new
// work and dropped what it had would be a kill, and one that kept its work and
// took more would not have left.
func TestADrainedReplicaTakesNoNewWorkButKeepsCountingWhatItHas(t *testing.T) {
	f := newFleet(t, "replica-0", "replica-1")
	finish, err := f.Dispatch("replica-0")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if err := f.Drain("replica-0"); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if _, present := f.State().Candidate("replica-0"); present {
		t.Error("a draining replica is still offered to the policy")
	}
	if _, present := f.State().Candidate("replica-1"); !present {
		t.Error("draining one replica took its sibling out of the snapshot too")
	}
	// A decision made from a snapshot taken a moment before the drain must not be
	// able to land. Without this refusal the drain would be a race: the operator
	// waits for the count to reach zero, and a request chosen just before the
	// drain arrives just after it.
	if _, err := f.Dispatch("replica-0"); !errors.Is(err, fleet.ErrOutOfRotation) {
		t.Errorf("dispatch to a draining replica: err = %v, want %v", err, fleet.ErrOutOfRotation)
	}

	// The request it was already serving is still counted against it: that count
	// reaching zero is how anyone knows the drain has finished.
	if got := member(t, f, "replica-0"); !got.Draining || got.Inflight != 1 {
		t.Errorf("draining replica reports draining=%v inflight=%d, want draining with its one request still counted", got.Draining, got.Inflight)
	}
	finish()
	if got := member(t, f, "replica-0").Inflight; got != 0 {
		t.Errorf("the drained replica's request finished but it still counts %d in flight", got)
	}
}

// A drain is undone by the operator who made it, and by nothing else.
func TestARestoredReplicaIsOfferedAgain(t *testing.T) {
	f := newFleet(t, "replica-0", "replica-1")
	if err := f.Drain("replica-0"); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if err := f.Restore("replica-0"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if _, present := f.State().Candidate("replica-0"); !present {
		t.Error("a restored replica is not offered to the policy")
	}
	if _, err := f.Dispatch("replica-0"); err != nil {
		t.Errorf("dispatch to a restored replica: %v", err)
	}
	if member(t, f, "replica-0").Draining {
		t.Error("a restored replica still reports draining")
	}
}

// Ejection is the health tracking's to make and its own to undo, and the number
// of times it happened is kept, because a sweep cell during which a replica was
// ejected measured a smaller fleet for part of its window and has to be able to
// say so.
func TestAnEjectedReplicaLeavesRotationUntilItIsReadmitted(t *testing.T) {
	f := newFleet(t, "replica-0", "replica-1")

	if ejected, err := f.Eject("replica-0"); err != nil || !ejected {
		t.Fatalf("eject: ejected=%v err=%v, want the replica taken out", ejected, err)
	}
	if _, present := f.State().Candidate("replica-0"); present {
		t.Error("an ejected replica is still offered to the policy")
	}
	if _, err := f.Dispatch("replica-0"); !errors.Is(err, fleet.ErrOutOfRotation) {
		t.Errorf("dispatch to an ejected replica: err = %v, want %v", err, fleet.ErrOutOfRotation)
	}
	// Every request that was in flight on a dead replica reports the same death.
	// That is one ejection, not one per request, or the count would measure the
	// load on the replica at the moment it died rather than how often it did.
	if again, _ := f.Eject("replica-0"); again {
		t.Error("ejecting a replica that was already out reported a second ejection")
	}
	if got := member(t, f, "replica-0"); !got.Ejected || got.Ejections != 1 {
		t.Errorf("replica reports ejected=%v ejections=%d, want ejected once", got.Ejected, got.Ejections)
	}

	if readmitted, err := f.Readmit("replica-0"); err != nil || !readmitted {
		t.Fatalf("readmit: readmitted=%v err=%v, want the replica back", readmitted, err)
	}
	if _, present := f.State().Candidate("replica-0"); !present {
		t.Error("a readmitted replica is not offered to the policy")
	}
	if got := member(t, f, "replica-0"); got.Ejected || got.Ejections != 1 {
		t.Errorf("readmitted replica reports ejected=%v ejections=%d, want back in with its one ejection still counted", got.Ejected, got.Ejections)
	}
}

// The two reasons a replica can be out are reversed by different parties, so
// neither undoes the other. A replica an operator drained and then stopped comes
// back healthy when it is restarted, and it must not rejoin the fleet because
// its health checks pass: the operator took it out, and only the operator puts
// it back.
func TestADrainedReplicaThatIsAlsoEjectedNeedsBothUndone(t *testing.T) {
	f := newFleet(t, "replica-0", "replica-1")
	if err := f.Drain("replica-0"); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := f.Eject("replica-0"); err != nil {
		t.Fatalf("eject: %v", err)
	}

	if _, err := f.Readmit("replica-0"); err != nil {
		t.Fatalf("readmit: %v", err)
	}
	if _, present := f.State().Candidate("replica-0"); present {
		t.Error("passing its health checks put a drained replica back in rotation")
	}

	if err := f.Restore("replica-0"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, present := f.State().Candidate("replica-0"); !present {
		t.Error("a replica both readmitted and restored is still out of rotation")
	}

	// And the other way round: restoring a replica that is still dead does not
	// put a dead replica back.
	if _, err := f.Eject("replica-0"); err != nil {
		t.Fatalf("eject: %v", err)
	}
	if err := f.Restore("replica-0"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, present := f.State().Candidate("replica-0"); present {
		t.Error("restoring a replica that is still ejected put it back in rotation")
	}
}

// An operator naming a replica the fleet does not have has mistyped it, and a
// silent success would leave them waiting on a drain that is not happening.
func TestDrainingAReplicaTheFleetDoesNotHaveIsAnError(t *testing.T) {
	f := newFleet(t, "replica-0")

	if err := f.Drain("replica-9"); err == nil {
		t.Error("draining a replica the fleet does not have succeeded")
	}
	if err := f.Restore("replica-9"); err == nil {
		t.Error("restoring a replica the fleet does not have succeeded")
	}
	if _, err := f.Eject("replica-9"); err == nil {
		t.Error("ejecting a replica the fleet does not have succeeded")
	}
	if _, err := f.Readmit("replica-9"); err == nil {
		t.Error("readmitting a replica the fleet does not have succeeded")
	}
}
