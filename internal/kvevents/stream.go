package kvevents

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// Message is one batch as it came off the wire: the publisher's sequence
// number, and the payload still encoded.
type Message struct {
	Seq     uint64
	Payload []byte
}

// Transport is how a stream reaches one replica's publisher. The ZeroMQ one is
// what the router runs; tests substitute their own.
type Transport interface {
	// Subscribe connects to the live stream.
	Subscribe(ctx context.Context) (Subscription, error)
	// Replay asks the publisher for every batch it still buffers from sequence
	// number from onwards, in order. What it returns can start later than from:
	// the buffer holds a fixed number of recent batches, and anything older is
	// gone.
	Replay(ctx context.Context, from uint64) ([]Message, error)
}

// Subscription is one connection to the live stream.
type Subscription interface {
	// Next blocks until the next message arrives, the connection ends, or ctx
	// is done.
	Next(ctx context.Context) (Message, error)
	Close() error
}

// Sink is what a stream keeps up to date: one replica's residency.
type Sink interface {
	// Apply folds one batch in, in sequence order.
	Apply(Batch)
	// Reset forgets everything the sink holds for the replica. The stream calls
	// it whenever what the sink holds may include blocks the engine has since
	// let go and no event will say so.
	Reset()
}

// DefaultReconnectDelay is how long a stream waits before reconnecting.
const DefaultReconnectDelay = time.Second

// Stream keeps one replica's residency up to date from its engine's events.
//
// The engine's publisher carries no initial state: a subscriber sees what is
// published after it connects, and nothing about the blocks stored before. Two
// things narrow that. The publisher numbers its batches, so a missing one is
// visible as a jump in the sequence; and it keeps the most recent batches in a
// replay buffer, so a subscriber can ask for what it missed. What neither can
// do is recover history older than the buffer, and the stream's answer then is
// to forget the replica's residency rather than keep believing in it: the
// missing batches may have removed blocks the sink still holds, and a belief in
// an evicted block misroutes, where a forgotten one only forfeits a match.
type Stream struct {
	Transport Transport
	Sink      Sink
	// ReconnectDelay is how long to wait before reconnecting after a
	// connection fails or ends. Zero is DefaultReconnectDelay.
	ReconnectDelay time.Duration
	// Log receives the stream's reconnects and losses. Nil discards them.
	Log *slog.Logger

	connected   atomic.Bool
	connections atomic.Uint64
	applied     atomic.Uint64
	replayed    atomic.Uint64
	lost        atomic.Uint64
	resets      atomic.Uint64
	undecodable atomic.Uint64
}

// Stats is what a stream reports about how complete its replica's residency is.
//
// It is the evidence for the cold-start limitation (#24): a router whose
// residency began after the engine's did, or lost history mid-run, reports it
// here rather than routing on an index that silently knows less than it claims.
type Stats struct {
	// Connected is whether the stream is subscribed right now.
	Connected bool `json:"connected"`
	// Connections is how many times it has subscribed. Each one started the
	// replica's residency from nothing, because a new connection vouches for
	// nothing an earlier one reported.
	Connections uint64 `json:"connections"`
	// Applied is batches folded into the residency, and Replayed how many of
	// those came from the publisher's replay buffer rather than the live stream.
	Applied  uint64 `json:"applied"`
	Replayed uint64 `json:"replayed"`
	// Lost is batches the stream never received and could not recover: history
	// older than the replay buffer. Resets is how many times losing some forced
	// the residency built so far to be thrown away.
	Lost   uint64 `json:"lost"`
	Resets uint64 `json:"resets"`
	// Undecodable is batches that arrived and could not be read. Each is also
	// counted as lost.
	Undecodable uint64 `json:"undecodable"`
}

// Stats reports the stream's state.
func (s *Stream) Stats() Stats {
	return Stats{
		Connected:   s.connected.Load(),
		Connections: s.connections.Load(),
		Applied:     s.applied.Load(),
		Replayed:    s.replayed.Load(),
		Lost:        s.lost.Load(),
		Resets:      s.resets.Load(),
		Undecodable: s.undecodable.Load(),
	}
}

// Run follows the replica's events until ctx is done, reconnecting whenever a
// connection fails or ends.
func (s *Stream) Run(ctx context.Context) error {
	delay := cmp.Or(s.ReconnectDelay, DefaultReconnectDelay)
	for {
		err := s.follow(ctx)
		s.connected.Store(false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log().Warn("KV event stream ended, reconnecting", "err", err, "in", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// follow is one connection's life: subscribe, catch up on what the publisher
// still buffers, then apply live batches as they come, filling any gap from the
// replay buffer.
func (s *Stream) follow(ctx context.Context) error {
	sub, err := s.Transport.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer sub.Close()
	s.connections.Add(1)
	s.connected.Store(true)

	// The engine may have restarted since the last connection, with an empty
	// cache and its sequence back at zero, and nothing on the wire says so.
	c := &catchUp{stream: s}
	c.reset()
	// Subscribed first and replayed second, so that nothing published in
	// between falls into neither: a batch published before the replay is served
	// is in the buffer, and one published after it arrives live.
	c.replay(ctx, nil)
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		switch {
		case msg.Seq < c.next:
			// Already applied, from the replay buffer.
		case msg.Seq > c.next:
			c.replay(ctx, &msg)
		default:
			c.apply(msg)
		}
	}
}

// catchUp is one connection's position in the sequence.
type catchUp struct {
	stream *Stream
	// next is the sequence number wanted next.
	next uint64
	// clean is whether the sink has been given nothing since its last reset,
	// so that a loss arriving before anything was applied does not reset a sink
	// that holds nothing to forget.
	clean bool
}

// replay asks the publisher for everything from the first missing batch, and
// applies what it can. pending is the live batch that revealed the gap, when one
// did; it is applied in its place afterwards unless the replay already covered
// it.
func (c *catchUp) replay(ctx context.Context, pending *Message) {
	from := c.next
	buffered, err := c.stream.Transport.Replay(ctx, from)
	if err != nil {
		c.stream.log().Warn("could not replay missed KV events", "from", from, "err", err)
	}
	for _, m := range buffered {
		if m.Seq < c.next {
			continue
		}
		c.apply(m)
		c.stream.replayed.Add(1)
	}
	if pending != nil && pending.Seq >= c.next {
		c.apply(*pending)
	}
}

// apply decodes one message and hands it to the sink, first recording as lost
// any batches between the last one applied and this.
func (c *catchUp) apply(m Message) {
	if m.Seq > c.next {
		c.lose(m.Seq)
	}
	c.next = m.Seq + 1
	batch, err := Decode(m.Payload)
	if err != nil {
		// A batch that arrived and cannot be read is history lost like any
		// other: its removals will never be applied.
		c.stream.undecodable.Add(1)
		c.stream.log().Error("undecodable KV event batch", "seq", m.Seq, "err", err)
		c.next = m.Seq
		c.lose(m.Seq + 1)
		return
	}
	batch.Seq = m.Seq
	c.stream.Sink.Apply(batch)
	c.stream.applied.Add(1)
	c.clean = false
}

// lose records the batches from next up to seq as gone for good, and forgets
// the residency built on the history before them.
func (c *catchUp) lose(seq uint64) {
	missing := seq - c.next
	c.stream.lost.Add(missing)
	c.stream.log().Warn("KV event history lost; forgetting this replica's residency", "from", c.next, "batches", missing)
	c.next = seq
	if !c.clean {
		c.stream.resets.Add(1)
		c.reset()
	}
}

func (c *catchUp) reset() {
	c.stream.Sink.Reset()
	c.clean = true
}

func (s *Stream) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}
