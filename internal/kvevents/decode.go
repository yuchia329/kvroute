package kvevents

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Decode reads one batch payload as the engine's publisher encodes it.
//
// The envelope is msgspec's array form of KVEventBatch — timestamp, events, and
// the publisher's data-parallel rank — and each event is a map tagged with its
// type under "type". Fields this router does not read are skipped rather than
// refused, because the engine carries several for consumers other than this one.
func Decode(payload []byte) (Batch, error) {
	r := &reader{b: payload}
	fields, err := r.arrayLen()
	if err != nil {
		return Batch{}, fmt.Errorf("kvevents: batch envelope: %w", err)
	}
	if fields < 2 || fields > 3 {
		return Batch{}, fmt.Errorf("kvevents: batch envelope has %d fields, want timestamp, events and an optional rank", fields)
	}
	var batch Batch
	if batch.TS, err = r.float(); err != nil {
		return Batch{}, fmt.Errorf("kvevents: batch timestamp: %w", err)
	}
	count, err := r.arrayLen()
	if err != nil {
		return Batch{}, fmt.Errorf("kvevents: batch events: %w", err)
	}
	batch.Events = make([]Event, 0, count)
	for i := range count {
		event, err := r.event()
		if err != nil {
			return Batch{}, fmt.Errorf("kvevents: event %d: %w", i, err)
		}
		batch.Events = append(batch.Events, event)
	}
	// The rank says which data-parallel engine published the batch. This fleet
	// runs one engine per replica, so there is nothing to tell apart.
	if fields == 3 {
		if err := r.skip(); err != nil {
			return Batch{}, fmt.Errorf("kvevents: batch rank: %w", err)
		}
	}
	if r.off != len(r.b) {
		return Batch{}, fmt.Errorf("kvevents: %d bytes after the batch", len(r.b)-r.off)
	}
	return batch, nil
}

// event reads one tagged event map.
func (r *reader) event() (Event, error) {
	head, err := r.peek()
	if err != nil {
		return Event{}, err
	}
	if !isMap(head) {
		return Event{}, fmt.Errorf("an event encoded as msgpack type %#x rather than a map, which is not how vLLM 0.28.0 encodes one", head)
	}
	keys, err := r.mapLen()
	if err != nil {
		return Event{}, err
	}
	var (
		e    Event
		kind string
	)
	for range keys {
		key, err := r.str()
		if err != nil {
			return Event{}, fmt.Errorf("field name: %w", err)
		}
		switch key {
		case "type":
			kind, err = r.str()
		case "block_hashes":
			e.BlockHashes, err = r.hashes()
		case "parent_block_hash":
			if !r.null() {
				var parent uint64
				parent, err = r.hash()
				e.Parent = &parent
			}
		case "token_ids":
			e.TokenIDs, err = r.tokens()
		case "block_size":
			var size uint64
			size, err = r.uint()
			e.BlockSize = int(min(size, math.MaxInt32))
		case "medium":
			if !r.null() {
				e.Medium, err = r.str()
			}
		case "lora_id", "lora_name":
			// The id is deprecated in favour of the name and the engine still
			// sends both. Either one present means an adapter was in play.
			if !r.null() {
				e.LoRA = true
				err = r.skip()
			}
		case "extra_keys":
			if !r.null() {
				e.ExtraKeys, err = r.extraKeys()
			}
		default:
			err = r.skip()
		}
		if err != nil {
			return Event{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	switch kind {
	case Stored.String():
		e.Kind = Stored
	case Removed.String():
		e.Kind = Removed
	case Cleared.String():
		e.Kind = Cleared
	default:
		return Event{}, fmt.Errorf("unknown event type %q", kind)
	}
	return e, nil
}

// hashes reads a list of block hashes.
func (r *reader) hashes() ([]uint64, error) {
	n, err := r.arrayLen()
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, n)
	for range n {
		h, err := r.hash()
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// hash reads one block hash, in either of the two forms the engine can send.
//
// The default is an integer, the low 64 bits of the digest. With
// VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES=0 it sends the whole digest as bytes
// instead, and those are folded to the same 64 bits the engine would otherwise
// have sent — int.from_bytes(digest, "big") masked to 64 bits, which is the last
// eight bytes read big-endian — so that one block has one name whichever way
// the replica was configured.
func (r *reader) hash() (uint64, error) {
	c, err := r.peek()
	if err != nil {
		return 0, err
	}
	if c != 0xc4 && c != 0xc5 && c != 0xc6 {
		return r.uint()
	}
	digest, err := r.bin()
	if err != nil {
		return 0, err
	}
	var h uint64
	for _, b := range digest[max(0, len(digest)-8):] {
		h = h<<8 | uint64(b)
	}
	return h, nil
}

// extraKeys reads the per-block extra keys as whether each block has any. What
// the keys are does not matter here, only that a block carries some.
func (r *reader) extraKeys() ([]bool, error) {
	n, err := r.arrayLen()
	if err != nil {
		return nil, err
	}
	out := make([]bool, 0, n)
	for range n {
		if r.null() {
			out = append(out, false)
			continue
		}
		if err := r.skip(); err != nil {
			return nil, err
		}
		out = append(out, true)
	}
	return out, nil
}

func (r *reader) bin() ([]byte, error) {
	c, err := r.next()
	if err != nil {
		return nil, err
	}
	var n int
	switch c {
	case 0xc4:
		n, err = r.length(1)
	case 0xc5:
		n, err = r.length(2)
	case 0xc6:
		n, err = r.length(4)
	default:
		return nil, fmt.Errorf("msgpack type %#x where bytes were expected", c)
	}
	if err != nil {
		return nil, err
	}
	return r.take(n)
}

// tokens reads a list of token ids.
func (r *reader) tokens() ([]uint32, error) {
	n, err := r.arrayLen()
	if err != nil {
		return nil, err
	}
	out := make([]uint32, 0, n)
	for range n {
		id, err := r.uint()
		if err != nil {
			return nil, err
		}
		if id > math.MaxUint32 {
			return nil, fmt.Errorf("token id %d is wider than any vocabulary", id)
		}
		out = append(out, uint32(id))
	}
	return out, nil
}

// reader walks one msgpack payload. It reads only the types the engine's event
// encoding uses, and every length it is handed is checked against what is left
// of the payload before anything is allocated for it.
type reader struct {
	b   []byte
	off int
}

var errShort = errors.New("payload ends mid-value")

func (r *reader) peek() (byte, error) {
	if r.off >= len(r.b) {
		return 0, errShort
	}
	return r.b[r.off], nil
}

func (r *reader) next() (byte, error) {
	c, err := r.peek()
	if err == nil {
		r.off++
	}
	return c, err
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.b)-r.off {
		return nil, errShort
	}
	out := r.b[r.off : r.off+n]
	r.off += n
	return out, nil
}

// length reads a big-endian length of the given width.
func (r *reader) length(width int) (int, error) {
	raw, err := r.take(width)
	if err != nil {
		return 0, err
	}
	var n uint64
	for _, b := range raw {
		n = n<<8 | uint64(b)
	}
	// Every element of a container is at least one byte, and every byte of a
	// string is one byte, so a length longer than what remains is a corrupt
	// payload however it is read.
	if n > uint64(len(r.b)-r.off) {
		return 0, errShort
	}
	return int(n), nil
}

func isMap(c byte) bool { return c&0xf0 == 0x80 || c == 0xde || c == 0xdf }

func (r *reader) arrayLen() (int, error) {
	c, err := r.next()
	if err != nil {
		return 0, err
	}
	switch {
	case c&0xf0 == 0x90:
		return int(c & 0x0f), nil
	case c == 0xdc:
		return r.length(2)
	case c == 0xdd:
		return r.length(4)
	}
	return 0, fmt.Errorf("msgpack type %#x where an array was expected", c)
}

func (r *reader) mapLen() (int, error) {
	c, err := r.next()
	if err != nil {
		return 0, err
	}
	switch {
	case c&0xf0 == 0x80:
		return int(c & 0x0f), nil
	case c == 0xde:
		return r.length(2)
	case c == 0xdf:
		return r.length(4)
	}
	return 0, fmt.Errorf("msgpack type %#x where a map was expected", c)
}

func (r *reader) str() (string, error) {
	c, err := r.next()
	if err != nil {
		return "", err
	}
	var n int
	switch {
	case c&0xe0 == 0xa0:
		n = int(c & 0x1f)
	case c == 0xd9:
		n, err = r.length(1)
	case c == 0xda:
		n, err = r.length(2)
	case c == 0xdb:
		n, err = r.length(4)
	default:
		return "", fmt.Errorf("msgpack type %#x where a string was expected", c)
	}
	if err != nil {
		return "", err
	}
	raw, err := r.take(n)
	return string(raw), err
}

// null consumes a nil if one is next, and reports whether it did.
func (r *reader) null() bool {
	if c, err := r.peek(); err == nil && c == 0xc0 {
		r.off++
		return true
	}
	return false
}

// uint reads a non-negative integer in any of msgpack's widths.
func (r *reader) uint() (uint64, error) {
	c, err := r.next()
	if err != nil {
		return 0, err
	}
	if c <= 0x7f {
		return uint64(c), nil
	}
	if c >= 0xe0 {
		return 0, fmt.Errorf("negative integer %d where none can be", int8(c))
	}
	width := map[byte]int{0xcc: 1, 0xcd: 2, 0xce: 4, 0xcf: 8, 0xd0: 1, 0xd1: 2, 0xd2: 4, 0xd3: 8}[c]
	if width == 0 {
		return 0, fmt.Errorf("msgpack type %#x where an integer was expected", c)
	}
	raw, err := r.take(width)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, b := range raw {
		v = v<<8 | uint64(b)
	}
	// The signed forms are two's complement at their width: a set top bit is a
	// negative number.
	if c >= 0xd0 && raw[0]&0x80 != 0 {
		return 0, errors.New("negative integer where none can be")
	}
	return v, nil
}

// float reads a floating-point number.
func (r *reader) float() (float64, error) {
	c, err := r.next()
	if err != nil {
		return 0, err
	}
	switch c {
	case 0xcb:
		raw, err := r.take(8)
		if err != nil {
			return 0, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(raw)), nil
	case 0xca:
		raw, err := r.take(4)
		if err != nil {
			return 0, err
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(raw))), nil
	}
	return 0, fmt.Errorf("msgpack type %#x where a float was expected", c)
}

// skip steps over one value of any type.
func (r *reader) skip() error {
	c, err := r.next()
	if err != nil {
		return err
	}
	var n int
	switch {
	case c <= 0x7f, c >= 0xe0, c == 0xc0, c == 0xc2, c == 0xc3:
		return nil
	case c&0xf0 == 0x80:
		return r.skipN(2 * int(c&0x0f))
	case c&0xf0 == 0x90:
		return r.skipN(int(c & 0x0f))
	case c&0xe0 == 0xa0:
		_, err = r.take(int(c & 0x1f))
		return err
	case c == 0xcc, c == 0xd0:
		_, err = r.take(1)
	case c == 0xcd, c == 0xd1:
		_, err = r.take(2)
	case c == 0xce, c == 0xd2, c == 0xca:
		_, err = r.take(4)
	case c == 0xcf, c == 0xd3, c == 0xcb:
		_, err = r.take(8)
	case c == 0xc4, c == 0xd9:
		if n, err = r.length(1); err == nil {
			_, err = r.take(n)
		}
	case c == 0xc5, c == 0xda:
		if n, err = r.length(2); err == nil {
			_, err = r.take(n)
		}
	case c == 0xc6, c == 0xdb:
		if n, err = r.length(4); err == nil {
			_, err = r.take(n)
		}
	case c == 0xdc:
		if n, err = r.length(2); err == nil {
			err = r.skipN(n)
		}
	case c == 0xdd:
		if n, err = r.length(4); err == nil {
			err = r.skipN(n)
		}
	case c == 0xde:
		if n, err = r.length(2); err == nil {
			err = r.skipN(2 * n)
		}
	case c == 0xdf:
		if n, err = r.length(4); err == nil {
			err = r.skipN(2 * n)
		}
	case c >= 0xd4 && c <= 0xd8:
		// fixext: a type byte and 1, 2, 4, 8 or 16 bytes of data.
		_, err = r.take(1 + 1<<(c-0xd4))
	case c == 0xc7, c == 0xc8, c == 0xc9:
		if n, err = r.length(1 << (c - 0xc7)); err == nil {
			_, err = r.take(1 + n)
		}
	default:
		err = fmt.Errorf("msgpack type %#x is not one this decoder knows", c)
	}
	return err
}

func (r *reader) skipN(values int) error {
	for range values {
		if err := r.skip(); err != nil {
			return err
		}
	}
	return nil
}
