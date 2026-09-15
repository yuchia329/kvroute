package kvevents_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/kvevents"
)

// fakeTransport is one replica's publisher, scripted. Each subscription serves
// the next session's live messages and then either ends with that session's
// error or, when it has none, stays connected until the stream is stopped.
//
// buffers is the publisher's replay buffer as it stands at each replay request
// in turn, the last one standing for every request after it. Replay serves the
// messages in it from the sequence number asked for, as the publisher does.
type fakeTransport struct {
	mu       sync.Mutex
	sessions []session
	buffers  [][]kvevents.Message
	replayed []uint64
	// replayErr, when set, is what every replay request fails with: a
	// publisher with no replay socket, or one that did not answer.
	replayErr error
}

type session struct {
	live []kvevents.Message
	end  error
}

func (f *fakeTransport) Subscribe(ctx context.Context) (kvevents.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil, fmt.Errorf("no publisher")
	}
	s := f.sessions[0]
	f.sessions = f.sessions[1:]
	return &fakeSubscription{live: s.live, end: s.end}, nil
}

func (f *fakeTransport) Replay(ctx context.Context, from uint64) ([]kvevents.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayed = append(f.replayed, from)
	if f.replayErr != nil {
		return nil, f.replayErr
	}
	var buffer []kvevents.Message
	if len(f.buffers) > 0 {
		buffer = f.buffers[0]
		if len(f.buffers) > 1 {
			f.buffers = f.buffers[1:]
		}
	}
	var out []kvevents.Message
	for _, m := range buffer {
		if m.Seq >= from {
			out = append(out, m)
		}
	}
	return out, nil
}

// replays is the sequence numbers the stream asked the publisher to replay
// from, in order.
func (f *fakeTransport) replays() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replayed)
}

type fakeSubscription struct {
	live []kvevents.Message
	end  error
}

func (s *fakeSubscription) Next(ctx context.Context) (kvevents.Message, error) {
	if len(s.live) > 0 {
		m := s.live[0]
		s.live = s.live[1:]
		return m, nil
	}
	if s.end != nil {
		return kvevents.Message{}, s.end
	}
	<-ctx.Done()
	return kvevents.Message{}, ctx.Err()
}

func (s *fakeSubscription) Close() error { return nil }

// recorder is the sink, and keeps what the stream did to it as one line per
// call so a test can say what should have happened in order.
type recorder struct {
	mu    sync.Mutex
	lines []string
	wake  chan struct{}
}

func newRecorder() *recorder { return &recorder{wake: make(chan struct{}, 1)} }

func (r *recorder) Apply(b kvevents.Batch) { r.log(fmt.Sprintf("apply %d", b.Seq)) }
func (r *recorder) Reset()                 { r.log("reset") }

func (r *recorder) log(line string) {
	r.mu.Lock()
	r.lines = append(r.lines, line)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// waitFor blocks until the sink has seen exactly want, failing if it sees
// something else or nothing more arrives in time.
func (r *recorder) waitFor(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		got := slices.Clone(r.lines)
		r.mu.Unlock()
		if len(got) >= len(want) {
			if !slices.Equal(got, want) {
				t.Fatalf("the sink saw\n  %s\nwant\n  %s", strings.Join(got, ", "), strings.Join(want, ", "))
			}
			return
		}
		select {
		case <-r.wake:
		case <-deadline:
			t.Fatalf("the sink saw only\n  %s\nwant\n  %s", strings.Join(got, ", "), strings.Join(want, ", "))
		}
	}
}

// messages returns one message per sequence number, each carrying a real
// engine payload.
func messages(t *testing.T, seqs ...uint64) []kvevents.Message {
	t.Helper()
	payload := golden(t, "cleared.msgpack")
	out := make([]kvevents.Message, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, kvevents.Message{Seq: seq, Payload: payload})
	}
	return out
}

func span(from, to uint64) []uint64 {
	var out []uint64
	for seq := from; seq <= to; seq++ {
		out = append(out, seq)
	}
	return out
}

// run starts a stream and stops it when the test ends.
func run(t *testing.T, tr kvevents.Transport, sink *recorder) *kvevents.Stream {
	t.Helper()
	s := &kvevents.Stream{Transport: tr, Sink: sink, ReconnectDelay: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return s
}

// The ordinary case: a fresh engine's batches arrive in order and are applied
// in order. The reset comes first because a new connection vouches for nothing
// an earlier one reported.
func TestBatchesAreAppliedInTheOrderTheEnginePublishedThem(t *testing.T) {
	tr := &fakeTransport{sessions: []session{{live: messages(t, 0, 1, 2)}}}
	sink := newRecorder()
	run(t, tr, sink)

	sink.waitFor(t, "reset", "apply 0", "apply 1", "apply 2")
}

// A gap in the live stream — the publisher's high-water mark dropped batches
// the router had not read — is filled from the publisher's replay buffer, and
// the batch that revealed the gap is applied once, in its place.
func TestAGapIsFilledFromTheReplayBuffer(t *testing.T) {
	tr := &fakeTransport{
		sessions: []session{{live: messages(t, 0, 1, 4, 5)}},
		// Empty when the stream connects, because the engine has published
		// nothing yet; holding the whole run by the time the gap appears.
		buffers: [][]kvevents.Message{nil, messages(t, span(0, 5)...)},
	}
	sink := newRecorder()
	run(t, tr, sink)

	sink.waitFor(t, "reset", "apply 0", "apply 1", "apply 2", "apply 3", "apply 4", "apply 5")
	if got, want := tr.replays(), []uint64{0, 2}; !slices.Equal(got, want) {
		t.Errorf("asked the publisher to replay from %v, want %v: once on connecting, then from the first missing batch", got, want)
	}
}

// A gap older than the replay buffer cannot be filled. The missing batches may
// have removed blocks the index still believes in, so the replica's residency is
// reset and rebuilt from what is left. That can only forget blocks the engine
// holds, never keep one it dropped.
func TestAGapOlderThanTheReplayBufferResetsTheReplica(t *testing.T) {
	tr := &fakeTransport{
		sessions: []session{{live: messages(t, 0, 1, 9, 10)}},
		buffers:  [][]kvevents.Message{nil, messages(t, 7, 8, 9, 10)},
	}
	sink := newRecorder()
	s := run(t, tr, sink)

	sink.waitFor(t, "reset", "apply 0", "apply 1", "reset", "apply 7", "apply 8", "apply 9", "apply 10")
	if got := s.Stats().Lost; got != 5 {
		t.Errorf("lost = %d batches, want the 5 between 2 and 6", got)
	}
}

// The cold start. A router that subscribes after the engine has been publishing
// — it restarted, or the fleet came up first — recovers what the replay buffer
// still holds and nothing older. The older history is counted as lost, because
// it is: blocks stored then are held by the engine and unknown to the index. No
// reset is spent on it, since the index held nothing to forget.
func TestJoiningLateRecoversTheBufferAndCountsTheRestAsLost(t *testing.T) {
	tr := &fakeTransport{
		sessions: []session{{live: messages(t, 50, 51)}},
		buffers:  [][]kvevents.Message{messages(t, span(40, 50)...)},
	}
	sink := newRecorder()
	s := run(t, tr, sink)

	want := []string{"reset"}
	for seq := 40; seq <= 51; seq++ {
		want = append(want, fmt.Sprintf("apply %d", seq))
	}
	sink.waitFor(t, want...)
	if got := s.Stats(); got.Lost != 40 || got.Resets != 0 || got.Replayed != 11 {
		t.Errorf("stats = %+v, want 40 lost, 0 resets and 11 replayed", got)
	}
}

// A connection that ends may be an engine that restarted, with an empty cache
// and its sequence back at zero, and nothing on the wire says which. So the
// stream reconnects and starts the replica from nothing.
func TestAReconnectStartsTheReplicaFromNothing(t *testing.T) {
	tr := &fakeTransport{sessions: []session{
		{live: messages(t, 0, 1), end: io.EOF},
		{live: messages(t, 0, 1)},
	}}
	sink := newRecorder()
	s := run(t, tr, sink)

	sink.waitFor(t, "reset", "apply 0", "apply 1", "reset", "apply 0", "apply 1")
	if got := s.Stats().Connections; got != 2 {
		t.Errorf("connections = %d, want 2", got)
	}
}

// A batch that arrived and cannot be read is history lost like any other: its
// removals will never be applied, so what the index holds may be stale.
func TestAnUnreadableBatchIsLostHistory(t *testing.T) {
	live := messages(t, 0, 1, 2)
	live[1].Payload = []byte{0xc1}
	tr := &fakeTransport{sessions: []session{{live: live}}}
	sink := newRecorder()
	s := run(t, tr, sink)

	sink.waitFor(t, "reset", "apply 0", "reset", "apply 2")
	if got := s.Stats(); got.Undecodable != 1 || got.Lost != 1 || got.Resets != 1 {
		t.Errorf("stats = %+v, want 1 undecodable, 1 lost and 1 reset", got)
	}
}

// A publisher with no replay socket cannot fill a gap at all, so every gap is
// lost history.
func TestAGapThatCannotBeReplayedResetsTheReplica(t *testing.T) {
	tr := &fakeTransport{
		sessions:  []session{{live: messages(t, 0, 1, 5)}},
		replayErr: errors.New("no replay socket"),
	}
	sink := newRecorder()
	s := run(t, tr, sink)

	sink.waitFor(t, "reset", "apply 0", "apply 1", "reset", "apply 5")
	if got := s.Stats().Lost; got != 3 {
		t.Errorf("lost = %d batches, want the 3 between 2 and 4", got)
	}
}
