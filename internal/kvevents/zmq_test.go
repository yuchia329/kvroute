package kvevents_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/kvevents"
)

// libzmqEnv turns on the tests that run the ZeroMQ client against a real
// libzmq. They are the only check on the wire half of the stream that a fake
// could not share a mistake with — a handshake or framing detail this client
// gets wrong would be one a fake written by the same hand got wrong too — and
// they need uv to fetch the engine's own pyzmq, so they are opt-in:
//
//	KVROUTE_LIBZMQ=1 go test ./internal/kvevents/
const libzmqEnv = "KVROUTE_LIBZMQ"

// publisher is testdata/publisher.py, running: a real libzmq PUB and ROUTER
// pair speaking the engine's protocol.
type publisher struct {
	endpoint, replay string
	stdin            io.WriteCloser
	out              *bufio.Scanner
}

func startPublisher(t *testing.T) *publisher {
	t.Helper()
	if os.Getenv(libzmqEnv) == "" {
		t.Skipf("set %s=1 to run against a real libzmq (needs uv)", libzmqEnv)
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Fatalf("%s is set but uv is not on PATH: %v", libzmqEnv, err)
	}
	cmd := exec.Command(uv, "run", "--no-project", "--with", "pyzmq==27.2.0", "--with", "msgspec==0.21.1", "python", "publisher.py")
	cmd.Dir = "testdata"
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the publisher: %v", err)
	}
	t.Cleanup(func() {
		fmt.Fprintln(stdin, "quit")
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	out := bufio.NewScanner(stdout)
	if !out.Scan() {
		t.Fatalf("the publisher printed nothing: %s", stderr.String())
	}
	var ready struct{ Endpoint, Replay string }
	if err := json.Unmarshal(out.Bytes(), &ready); err != nil {
		t.Fatalf("the publisher said %q: %v", out.Text(), err)
	}
	return &publisher{endpoint: ready.Endpoint, replay: ready.Replay, stdin: stdin, out: out}
}

// do sends the publisher one command and returns the sequence number it will
// publish next.
func (p *publisher) do(t *testing.T, command string, batches int) uint64 {
	t.Helper()
	fmt.Fprintf(p.stdin, "%s %d\n", command, batches)
	if !p.out.Scan() {
		t.Fatalf("the publisher did not acknowledge %s %d", command, batches)
	}
	var ack struct{ Next uint64 }
	if err := json.Unmarshal(p.out.Bytes(), &ack); err != nil {
		t.Fatalf("the publisher acknowledged with %q: %v", p.out.Text(), err)
	}
	return ack.Next
}

// Replay is one request to the engine's ROUTER socket and a stream of replies
// ending in a marker, over a DEALER connection this client speaks itself.
func TestReplayReadsARealPublishersBuffer(t *testing.T) {
	p := startPublisher(t)
	p.do(t, "drop", 5)

	z := &kvevents.ZMQ{Endpoint: p.endpoint, ReplayEndpoint: p.replay}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := z.Replay(ctx, 2)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	var seqs []uint64
	for _, m := range got {
		seqs = append(seqs, m.Seq)
		batch, err := kvevents.Decode(m.Payload)
		if err != nil {
			t.Fatalf("batch %d: %v", m.Seq, err)
		}
		// publisher.py names batch n's one block n + 1.
		if want := []uint64{m.Seq + 1}; !slices.Equal(batch.Events[0].BlockHashes, want) {
			t.Errorf("batch %d carried block %v, want %v", m.Seq, batch.Events[0].BlockHashes, want)
		}
	}
	if want := []uint64{2, 3, 4}; !slices.Equal(seqs, want) {
		t.Errorf("replayed %v, want %v", seqs, want)
	}
}

// The whole wire, end to end: subscribe to a real PUB socket, fall behind, and
// catch up from the real replay buffer without losing anything.
func TestAStreamFollowsARealPublisherAcrossAGap(t *testing.T) {
	p := startPublisher(t)
	sink := newRecorder()
	s := run(t, &kvevents.ZMQ{Endpoint: p.endpoint, ReplayEndpoint: p.replay}, sink)

	// A subscription takes effect a moment after it is made — ZeroMQ's slow
	// joiner — and a batch published before then never reaches this subscriber
	// live. The stream recovers such a batch from replay once a later one
	// arrives to reveal the gap, so batches go out one at a time until one has
	// been applied.
	deadline := time.Now().Add(10 * time.Second)
	for s.Stats().Applied == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("nothing was applied from a real publisher: %+v", s.Stats())
		}
		p.do(t, "publish", 1)
		time.Sleep(20 * time.Millisecond)
	}

	// Now a real gap: three batches the subscriber never sees live, then two
	// it does.
	p.do(t, "drop", 3)
	next := p.do(t, "publish", 2)

	want := []string{"reset"}
	for seq := range next {
		want = append(want, fmt.Sprintf("apply %d", seq))
	}
	sink.waitFor(t, want...)
	if got := s.Stats(); got.Lost != 0 || got.Resets != 0 || got.Replayed < 3 {
		t.Errorf("stats = %+v, want nothing lost, no resets and the three dropped batches replayed", got)
	}
}
