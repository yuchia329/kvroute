package fleet

import (
	"errors"
	"fmt"
)

// ErrOutOfRotation is a dispatch to a replica that left rotation after the
// snapshot the decision was made from was taken.
//
// It is an error the router answers by choosing again from a fresh snapshot,
// not by dropping the request: the replica is still there to be asked, and the
// request has been sent nowhere yet.
var ErrOutOfRotation = errors.New("replica is out of rotation")

// rotation is why a replica is not being offered to the policy, when it is not.
//
// Two reasons, kept apart because different parties reverse them. A drain is an
// operator's decision and only the operator undoes it; an ejection is the health
// tracking's, and it undoes itself when the replica answers again. A replica
// that was drained and then stopped comes back healthy when it is restarted, and
// one flag could not say that it must nevertheless stay out.
type rotation struct {
	// draining is an operator having taken the replica out: it gets no new work
	// and finishes what it has.
	draining bool
	// ejected is the health tracking having taken the replica out because it
	// stopped answering, either to a health check or to a real request.
	ejected bool
	// ejections counts the times the replica went from in to out by ejection.
	ejections int
}

func (r rotation) in() bool { return !r.draining && !r.ejected }

// Member is one replica as the fleet reports it, whether or not it is in
// rotation.
//
// It is what State is not. State is the snapshot a policy decides from, and a
// replica out of rotation is simply absent from it, which is how its leaving is
// expressed to a policy. Anything reporting on the fleet — the router's stats,
// an operator waiting for a drain to finish — needs the absent replicas as much
// as the present ones, and their in-flight counts most of all.
type Member struct {
	Candidate
	// Draining is an operator having taken the replica out of rotation.
	Draining bool
	// Ejected is the health tracking having taken it out because it stopped
	// answering.
	Ejected bool
	// Ejections is how many times the replica has been ejected since the fleet
	// was built. It is a count of transitions, not of failures: every request in
	// flight on a replica that dies reports the same death, and the count would
	// otherwise measure how loaded the replica was when it died.
	Ejections int
}

// Members returns every replica the fleet fronts, in rotation or not.
func (f *Fleet) Members() []Member {
	f.mu.RLock()
	defer f.mu.RUnlock()

	members := make([]Member, len(f.replicas))
	for i := range f.replicas {
		r := f.rotation[i]
		members[i] = Member{Candidate: f.candidate(i), Draining: r.draining, Ejected: r.ejected, Ejections: r.ejections}
	}
	return members
}

// Drain takes a replica out of rotation gracefully: from the moment it returns,
// no new request is dispatched to it, and the requests it is already serving are
// left alone to finish.
//
// It takes the fleet's write lock, which is what makes that "from the moment it
// returns" exact. Dispatch holds the read lock across its check and its count,
// so every dispatch either counted itself before the drain or is refused after
// it. The replica's in-flight count can therefore only fall once this returns,
// and its reaching zero is the drain finishing.
func (f *Fleet) Drain(id string) error {
	return f.change(id, "drain", func(r *rotation) { r.draining = true })
}

// Restore puts a drained replica back into rotation.
//
// It is the only undo a drain has. The drain was an operator's decision rather
// than a symptom, so nothing the fleet observes about the replica afterwards —
// its health checks passing again included — puts it back on its own. A replica
// that is also ejected stays out until it is readmitted as well.
func (f *Fleet) Restore(id string) error {
	return f.change(id, "restore", func(r *rotation) { r.draining = false })
}

// Eject takes a replica out of rotation because it has stopped answering, and
// reports whether this call is what took it out.
//
// A replica already ejected is not ejected again. The requests that were in
// flight on a replica when it died all find out at once, and each of them
// reporting it must still come to one ejection.
func (f *Fleet) Eject(id string) (bool, error) {
	var ejected bool
	err := f.change(id, "eject", func(r *rotation) {
		if !r.ejected {
			r.ejected, r.ejections = true, r.ejections+1
			ejected = true
		}
	})
	return ejected, err
}

// Readmit puts an ejected replica back into rotation once it answers again, and
// reports whether this call is what brought it back. A replica that is also
// draining stays out until it is restored as well.
func (f *Fleet) Readmit(id string) (bool, error) {
	var readmitted bool
	err := f.change(id, "readmit", func(r *rotation) {
		if r.ejected {
			r.ejected = false
			readmitted = true
		}
	})
	return readmitted, err
}

// change applies one transition to one replica's rotation under the write lock.
// An unknown id is an error named for the operation that was attempted.
func (f *Fleet) change(id, operation string, transition func(*rotation)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.index[id]
	if !ok {
		return fmt.Errorf("fleet: no replica %q to %s", id, operation)
	}
	transition(&f.rotation[i])
	return nil
}
