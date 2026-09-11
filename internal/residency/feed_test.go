package residency_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/kvevents"
	"github.com/yuchia329/kvroute/internal/residency"
)

// publisherOf is one replica's publisher: a live stream of the given messages,
// and an empty replay buffer.
type publisherOf []kvevents.Message

func (p publisherOf) Subscribe(context.Context) (kvevents.Subscription, error) {
	return &liveOf{messages: p}, nil
}

func (publisherOf) Replay(context.Context, uint64) ([]kvevents.Message, error) { return nil, nil }

type liveOf struct{ messages []kvevents.Message }

func (l *liveOf) Next(ctx context.Context) (kvevents.Message, error) {
	if len(l.messages) > 0 {
		m := l.messages[0]
		l.messages = l.messages[1:]
		return m, nil
	}
	<-ctx.Done()
	return kvevents.Message{}, ctx.Err()
}

func (*liveOf) Close() error { return nil }

// silent is a publisher that never answers.
type silent struct{}

func (silent) Subscribe(context.Context) (kvevents.Subscription, error) {
	return nil, errors.New("connection refused")
}

func (silent) Replay(context.Context, uint64) ([]kvevents.Message, error) { return nil, nil }

// storedTokens is what the engine stored in kvevents/testdata/stored.msgpack:
// two blocks, written out as gen.py writes them.
var storedTokens = []uint32{
	128000, 128006, 9125, 128007, 271, 0, 1, 127, 128, 255, 256, 65535, 65536, 128255, 42, 2,
	100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115,
}

func feed(t *testing.T, ix *residency.Index, publishers map[string]kvevents.Transport) *residency.Feed {
	t.Helper()
	f, err := residency.NewFeed(ix, publishers, nil)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return f
}

// Each replica's events feed that replica's residency and no other's. Applying
// one engine's report to another replica would route a request to a replica
// that must prefill it anyway, with nothing in the numbers to say why.
func TestAFeedAppliesEachReplicasEventsToThatReplica(t *testing.T) {
	payload, err := os.ReadFile("../kvevents/testdata/stored.msgpack")
	if err != nil {
		t.Fatal(err)
	}
	ix := index(t, "replica-0", "replica-1")
	f := feed(t, ix, map[string]kvevents.Transport{
		"replica-0": publisherOf(nil),
		"replica-1": publisherOf{{Seq: 0, Payload: payload}},
	})

	deadline := time.Now().Add(2 * time.Second)
	for matchOf(t, ix, "replica-1", storedTokens) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("replica-1 never came to hold what its engine reported: %+v", f.Stats())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := matchOf(t, ix, "replica-0", storedTokens); got != 0 {
		t.Errorf("replica-0 matched %d blocks of replica-1's report", got)
	}

	stats := f.Stats()
	if got := []string{stats[0].Replica, stats[1].Replica}; !slices.Equal(got, []string{"replica-0", "replica-1"}) {
		t.Fatalf("stats are for %v, want replica-0 then replica-1", got)
	}
	if got := stats[1]; got.Blocks != 2 || got.Stream.Applied != 1 || !got.Stream.Connected {
		t.Errorf("replica-1's stats = %+v, want 2 blocks held from 1 applied batch on a connected stream", got)
	}
}

// A router whose publishers are not answering would route every request cold
// and report it as the exact policy's result. Ready is what refuses to start it
// that way, and it names the replicas that never answered.
func TestReadyNamesTheReplicasWhosePublishersNeverAnswered(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	f := feed(t, ix, map[string]kvevents.Transport{
		"replica-0": publisherOf(nil),
		"replica-1": silent{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := f.Ready(ctx)
	if err == nil {
		t.Fatal("Ready returned with replica-1's publisher never answering")
	}
	if !strings.Contains(err.Error(), "replica-1") || strings.Contains(err.Error(), "replica-0") {
		t.Errorf("Ready said %q, want it to name replica-1 and only replica-1", err)
	}
}

// Every replica the index covers needs a stream, and every stream a replica the
// index covers: a replica with no stream would never hold anything, so the
// policy would never take affinity to it, silently.
func TestAFeedRefusesReplicasItHasNoStreamFor(t *testing.T) {
	ix := index(t, "replica-0", "replica-1")
	if _, err := residency.NewFeed(ix, map[string]kvevents.Transport{"replica-0": publisherOf(nil)}, nil); err == nil {
		t.Error("a feed was built with no stream for replica-1")
	}
	if _, err := residency.NewFeed(ix, map[string]kvevents.Transport{
		"replica-0": publisherOf(nil), "replica-1": publisherOf(nil), "replica-9": publisherOf(nil),
	}, nil); err == nil {
		t.Error("a feed was built with a stream for replica-9, which the index does not cover")
	}
}
