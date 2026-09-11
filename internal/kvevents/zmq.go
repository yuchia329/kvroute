package kvevents

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// ZMQ reaches one replica's publisher over ZeroMQ.
//
// It speaks ZMTP 3.0 with the NULL security mechanism itself rather than
// binding libzmq: the router is cross-compiled without cgo, and the two socket
// roles it needs are ZeroMQ's simplest. As a SUB it subscribes and reads; as a
// DEALER it sends one replay request and reads the replies. Everything else
// ZeroMQ would do — reconnecting, queueing, high-water marks — is either the
// publisher's side of the conversation or Stream's job.
//
// It announces ZMTP 3.0 rather than 3.1 so that a subscription is the plain
// message 3.0 defines, which libzmq accepts from a 3.0 peer. The wire is tested
// against the libzmq the engine itself runs, not against a fake of it.
type ZMQ struct {
	// Endpoint is the PUB socket, as tcp://host:port.
	Endpoint string
	// ReplayEndpoint is the ROUTER socket that replays buffered batches, as
	// tcp://host:port. Empty when the publisher has none, and then every gap in
	// the stream is lost history.
	ReplayEndpoint string
	// ReplayTimeout bounds one replay request end to end. Zero is
	// DefaultReplayTimeout.
	ReplayTimeout time.Duration
}

// DefaultReplayTimeout bounds a replay request. A replay of the whole buffer is
// ten thousand batches over a local connection, which takes a small fraction of
// this; a publisher that has not finished in five seconds is not going to.
const DefaultReplayTimeout = 5 * time.Second

// handshakeTimeout bounds the ZMTP handshake, so that an endpoint that accepts
// the connection and then says nothing — something that is not a ZeroMQ
// publisher at all — fails rather than hangs.
const handshakeTimeout = 5 * time.Second

// maxFrame is the largest frame this client will allocate for. A batch carries
// the tokens of every block one engine step stored, which is kilobytes; a
// length past this is a corrupt stream rather than a batch.
const maxFrame = 256 << 20

// ErrNoReplay is what Replay returns for a publisher with no replay endpoint.
var ErrNoReplay = errors.New("kvevents: the publisher has no replay endpoint")

// endSeq is the sequence number the engine's replay service ends a reply with.
var endSeq = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// Subscribe connects to the PUB socket and subscribes to the topic.
func (z *ZMQ) Subscribe(ctx context.Context) (Subscription, error) {
	c, err := dial(ctx, z.Endpoint, "SUB", "PUB")
	if err != nil {
		return nil, err
	}
	// In ZMTP 3.0 a subscription is a message whose first byte is 1 and whose
	// remainder is the topic prefix. The prefix here is empty, which matches
	// every batch whatever topic the engine publishes under.
	if err := c.send(false, []byte{1}); err != nil {
		c.conn.Close()
		return nil, fmt.Errorf("kvevents: subscribe to %s: %w", z.Endpoint, err)
	}
	return &subscription{c: c}, nil
}

// Replay asks the ROUTER socket for every batch it buffers from from onwards.
//
// A reply cut short by the timeout returns what did arrive, with the error: the
// batches received are real, and the stream applies them and counts the rest as
// lost rather than throwing away a partial catch-up.
func (z *ZMQ) Replay(ctx context.Context, from uint64) ([]Message, error) {
	if z.ReplayEndpoint == "" {
		return nil, ErrNoReplay
	}
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(z.ReplayTimeout, DefaultReplayTimeout))
	defer cancel()
	c, err := dial(ctx, z.ReplayEndpoint, "DEALER", "ROUTER")
	if err != nil {
		return nil, err
	}
	defer c.conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}

	// The ROUTER reads a request as identity, empty delimiter, start. It adds
	// the identity itself; the delimiter is this DEALER's to send, as a REQ
	// socket would. A REQ socket cannot be used instead: it accepts one reply,
	// and the replay service sends one per buffered batch.
	var start [8]byte
	binary.BigEndian.PutUint64(start[:], from)
	if err := c.send(true, nil); err != nil {
		return nil, fmt.Errorf("kvevents: replay request to %s: %w", z.ReplayEndpoint, err)
	}
	if err := c.send(false, start[:]); err != nil {
		return nil, fmt.Errorf("kvevents: replay request to %s: %w", z.ReplayEndpoint, err)
	}

	var out []Message
	for {
		parts, err := c.message()
		if err != nil {
			return out, fmt.Errorf("kvevents: replay from %s after %d batches: %w", z.ReplayEndpoint, len(out), err)
		}
		// Each reply is empty delimiter, topic, sequence, payload; the last is
		// the same shape with an empty topic, a sequence of -1 and no payload.
		if len(parts) != 4 || len(parts[0]) != 0 || len(parts[2]) != 8 {
			return out, fmt.Errorf("kvevents: replay from %s sent a %d-part reply, not delimiter, topic, sequence and payload", z.ReplayEndpoint, len(parts))
		}
		if bytes.Equal(parts[2], endSeq) {
			return out, nil
		}
		out = append(out, Message{Seq: binary.BigEndian.Uint64(parts[2]), Payload: parts[3]})
	}
}

// subscription is one SUB connection.
type subscription struct{ c *conn }

func (s *subscription) Next(ctx context.Context) (Message, error) {
	// A read blocks until a batch arrives, and a quiet engine publishes
	// nothing, so ctx is honoured by expiring the read rather than by polling.
	_ = s.c.conn.SetReadDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { _ = s.c.conn.SetReadDeadline(time.Now()) })
	defer stop()

	parts, err := s.c.message()
	if err != nil {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		return Message{}, err
	}
	// The engine publishes topic, sequence, payload.
	if len(parts) != 3 || len(parts[1]) != 8 {
		return Message{}, fmt.Errorf("kvevents: a %d-part message, not topic, sequence and payload", len(parts))
	}
	return Message{Seq: binary.BigEndian.Uint64(parts[1]), Payload: parts[2]}, nil
}

func (s *subscription) Close() error { return s.c.conn.Close() }

// conn is one ZMTP connection, past its handshake.
type conn struct {
	conn net.Conn
	r    *bufio.Reader
}

// dial connects to a ZeroMQ endpoint and completes the ZMTP 3.0 handshake as
// socket type ours, refusing a peer that is not of type theirs.
func dial(ctx context.Context, endpoint, ours, theirs string) (*conn, error) {
	addr, found := strings.CutPrefix(endpoint, "tcp://")
	if !found || strings.HasPrefix(addr, "*") {
		return nil, fmt.Errorf("kvevents: endpoint %q: want tcp://host:port, the address the publisher can be reached at", endpoint)
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("kvevents: connect to %s: %w", endpoint, err)
	}
	c := &conn{conn: nc, r: bufio.NewReaderSize(nc, 64<<10)}
	_ = nc.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := c.handshake(ours, theirs); err != nil {
		nc.Close()
		return nil, fmt.Errorf("kvevents: handshake with %s: %w", endpoint, err)
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// handshake exchanges greetings and READY commands.
func (c *conn) handshake(ours, theirs string) error {
	// The greeting: signature, version 3.0, the NULL mechanism, not the
	// server, and zero filler to 64 bytes. It is sent whole rather than in the
	// stages libzmq uses, which the protocol allows.
	var greeting [64]byte
	greeting[0], greeting[9] = 0xff, 0x7f
	greeting[10], greeting[11] = 3, 0
	copy(greeting[12:32], "NULL")
	if _, err := c.conn.Write(greeting[:]); err != nil {
		return err
	}

	var peer [64]byte
	if _, err := io.ReadFull(c.r, peer[:]); err != nil {
		return fmt.Errorf("reading the peer's greeting: %w", err)
	}
	if peer[0] != 0xff || peer[9]&0x01 == 0 {
		return errors.New("the peer is not speaking ZMTP 3")
	}
	if peer[10] < 3 {
		return fmt.Errorf("the peer speaks ZMTP %d, and this client speaks 3", peer[10])
	}
	if mechanism := string(bytes.TrimRight(peer[12:32], "\x00")); mechanism != "NULL" {
		return fmt.Errorf("the peer wants the %s security mechanism, and this client speaks only NULL", mechanism)
	}

	ready := command("READY", "Socket-Type", ours)
	if ours == "DEALER" {
		// A DEALER names itself; an empty name has the ROUTER assign one.
		ready = append(ready, property("Identity", "")...)
	}
	if err := c.write(0x04, ready); err != nil {
		return err
	}

	isCommand, body, _, err := c.frame()
	if err != nil {
		return fmt.Errorf("reading the peer's READY: %w", err)
	}
	if !isCommand {
		return errors.New("the peer sent a message before its READY")
	}
	name, rest, err := commandName(body)
	if err != nil {
		return err
	}
	switch name {
	case "READY":
	case "ERROR":
		reason := rest
		if len(reason) > 0 {
			reason = reason[1:min(len(reason), 1+int(reason[0]))]
		}
		return fmt.Errorf("the peer refused the connection: %s", reason)
	default:
		return fmt.Errorf("the peer sent %s where READY was expected", name)
	}
	properties, err := parseProperties(rest)
	if err != nil {
		return err
	}
	if got := properties["Socket-Type"]; got != theirs && !(theirs == "PUB" && got == "XPUB") {
		return fmt.Errorf("the peer is a %s socket, and a %s connects to a %s", got, ours, theirs)
	}
	return nil
}

// command builds a command body: its name, then one property.
func command(name, key, value string) []byte {
	body := append([]byte{byte(len(name))}, name...)
	return append(body, property(key, value)...)
}

func property(key, value string) []byte {
	p := append([]byte{byte(len(key))}, key...)
	p = binary.BigEndian.AppendUint32(p, uint32(len(value)))
	return append(p, value...)
}

func commandName(body []byte) (string, []byte, error) {
	if len(body) == 0 || len(body) < 1+int(body[0]) {
		return "", nil, errors.New("a command with no name")
	}
	return string(body[1 : 1+body[0]]), body[1+body[0]:], nil
}

func parseProperties(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for len(b) > 0 {
		n := int(b[0])
		if len(b) < 1+n+4 {
			return nil, errors.New("a READY property cut short")
		}
		key := string(b[1 : 1+n])
		size := binary.BigEndian.Uint32(b[1+n : 1+n+4])
		b = b[1+n+4:]
		if uint64(len(b)) < uint64(size) {
			return nil, errors.New("a READY property cut short")
		}
		out[key] = string(b[:size])
		b = b[size:]
	}
	return out, nil
}

// send writes one message frame.
func (c *conn) send(more bool, body []byte) error {
	var flags byte
	if more {
		flags |= 0x01
	}
	return c.write(flags, body)
}

// write writes one frame with the given flags, choosing the short or long size
// form by length.
func (c *conn) write(flags byte, body []byte) error {
	var head []byte
	if len(body) > 0xff {
		head = binary.BigEndian.AppendUint64([]byte{flags | 0x02}, uint64(len(body)))
	} else {
		head = []byte{flags, byte(len(body))}
	}
	_, err := c.conn.Write(append(head, body...))
	return err
}

// frame reads one frame.
func (c *conn) frame() (isCommand bool, body []byte, more bool, err error) {
	flags, err := c.r.ReadByte()
	if err != nil {
		return false, nil, false, err
	}
	if flags&0xf8 != 0 {
		return false, nil, false, fmt.Errorf("a frame with reserved flags %#x set", flags)
	}
	var size uint64
	if flags&0x02 != 0 {
		var long [8]byte
		if _, err := io.ReadFull(c.r, long[:]); err != nil {
			return false, nil, false, err
		}
		size = binary.BigEndian.Uint64(long[:])
	} else {
		short, err := c.r.ReadByte()
		if err != nil {
			return false, nil, false, err
		}
		size = uint64(short)
	}
	if size > maxFrame {
		return false, nil, false, fmt.Errorf("a %d-byte frame, past the %d this client reads", size, maxFrame)
	}
	body = make([]byte, size)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return false, nil, false, err
	}
	return flags&0x04 != 0, body, flags&0x01 != 0, nil
}

// message reads one multipart message, skipping any command between messages.
func (c *conn) message() ([][]byte, error) {
	var parts [][]byte
	for {
		isCommand, body, more, err := c.frame()
		if err != nil {
			return nil, err
		}
		if isCommand {
			// Heartbeats and the like. The engine configures none, and none
			// carries a batch.
			continue
		}
		parts = append(parts, body)
		if !more {
			return parts, nil
		}
	}
}
