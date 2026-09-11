package residency

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/kvevents"
)

// Feed keeps an index current from every replica's KV cache event stream.
//
// It is the index and its streams as one thing, because neither means anything
// alone: an index with no stream holds nothing, and a stream is worth only what
// it puts into the index. It is also what the router reports, since how much a
// replica's engine holds and how complete the router's account of it is are two
// halves of one reading.
type Feed struct {
	index *Index
	// streams is one per replica, in the index's replica order.
	streams []replicaStream
}

type replicaStream struct {
	replica string
	stream  *kvevents.Stream
}

// FeedStats is one replica's residency and the stream it is built from.
type FeedStats struct {
	ReplicaStats
	Stream kvevents.Stats `json:"stream"`
}

// NewFeed builds a feed over an index, with one stream per replica the index
// covers and none for any replica it does not.
func NewFeed(index *Index, transports map[string]kvevents.Transport, log *slog.Logger) (*Feed, error) {
	f := &Feed{index: index}
	for _, id := range index.names {
		transport, present := transports[id]
		if !present {
			return nil, fmt.Errorf("residency: no KV event stream for %s, so the index would never hold anything for it and it would never be routed to on a match", id)
		}
		stream := &kvevents.Stream{Transport: transport, Sink: sink{index: index, replica: id}}
		if log != nil {
			stream.Log = log.With("replica", id)
		}
		f.streams = append(f.streams, replicaStream{replica: id, stream: stream})
	}
	for id := range transports {
		if _, covered := index.replicas[id]; !covered {
			return nil, fmt.Errorf("residency: a KV event stream for %s, which is not a replica the index covers", id)
		}
	}
	return f, nil
}

// Run follows every replica's stream until ctx is done.
func (f *Feed) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range f.streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.stream.Run(ctx)
		}()
	}
	wg.Wait()
}

// Ready waits until every replica's stream has connected at least once, and
// when ctx ends first, names the replicas whose publishers never answered.
//
// A router routing on streams that are not connected sends every request cold
// and reports that as the exact policy's result, so it is refused at startup
// rather than discovered in the decision mix of a finished grid.
func (f *Feed) Ready(ctx context.Context) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting []string
		for _, s := range f.streams {
			if s.stream.Stats().Connections == 0 {
				waiting = append(waiting, s.replica)
			}
		}
		if len(waiting) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("residency: no KV event stream from %s: is the fleet publishing its cache events (KV_EVENTS=1 in ops/versions.env)?", strings.Join(waiting, ", "))
		case <-tick.C:
		}
	}
}

// Stats reports every replica's residency and stream, in the index's order.
func (f *Feed) Stats() []FeedStats {
	held := f.index.Stats()
	out := make([]FeedStats, len(held))
	for i, h := range held {
		out[i] = FeedStats{ReplicaStats: h, Stream: f.streams[i].stream.Stats()}
	}
	return out
}

// sink applies one replica's stream to that replica's part of the index.
type sink struct {
	index   *Index
	replica string
}

func (s sink) Apply(b kvevents.Batch) { s.index.Apply(s.replica, b) }
func (s sink) Reset()                 { s.index.Reset(s.replica) }
