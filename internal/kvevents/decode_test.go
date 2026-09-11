package kvevents_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/kvevents"
)

// golden reads a payload the engine's own encoder produced. testdata/gen.py is
// how they were made and how to make them again.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading %s: %v (regenerate with testdata/gen.py)", name, err)
	}
	return b
}

// The tokens gen.py stores. Written out here rather than read back from
// anywhere, so that a decoder which mangles one integer width cannot agree with
// itself.
var (
	firstBlock  = []uint32{128000, 128006, 9125, 128007, 271, 0, 1, 127, 128, 255, 256, 65535, 65536, 128255, 42, 2}
	secondBlock = []uint32{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115}
)

// The event the whole policy is built on: the engine saying which blocks it now
// holds, which tokens they contain, and where the run started.
func TestAStoredEventDecodesAsTheEngineEncodesIt(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "stored.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if batch.TS != 1757500000.25 {
		t.Errorf("timestamp = %v, want 1757500000.25", batch.TS)
	}
	if len(batch.Events) != 1 {
		t.Fatalf("decoded %d events, want 1", len(batch.Events))
	}

	got := batch.Events[0]
	if got.Kind != kvevents.Stored {
		t.Errorf("kind = %v, want %v", got.Kind, kvevents.Stored)
	}
	if want := []uint64{0xDEADBEEF12345678, 0xFFFFFFFFFFFFFFFF}; !slices.Equal(got.BlockHashes, want) {
		t.Errorf("block hashes = %#x, want %#x", got.BlockHashes, want)
	}
	if got.Parent != nil {
		t.Errorf("a run from the start of the prompt has parent %#x, want none", *got.Parent)
	}
	if want := slices.Concat(firstBlock, secondBlock); !slices.Equal(got.TokenIDs, want) {
		t.Errorf("tokens = %v, want %v", got.TokenIDs, want)
	}
	if got.BlockSize != 16 {
		t.Errorf("block size = %d, want 16", got.BlockSize)
	}
	if got.Medium != "GPU" {
		t.Errorf("medium = %q, want GPU", got.Medium)
	}
}

// A run that continues one the engine already holds names the block it hangs
// off. Without that link the stored tokens are a fragment of some prompt with no
// way to say which, and a prefix cache only ever serves whole leading runs.
func TestAStoredEventNamesTheBlockItContinues(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "stored_child.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := batch.Events[0]
	if got.Parent == nil || *got.Parent != 0xFFFFFFFFFFFFFFFF {
		t.Fatalf("parent = %v, want 0xffffffffffffffff", got.Parent)
	}
	if want := []uint64{7}; !slices.Equal(got.BlockHashes, want) {
		t.Errorf("block hashes = %v, want %v", got.BlockHashes, want)
	}
	if !slices.Equal(got.TokenIDs, secondBlock) {
		t.Errorf("tokens = %v, want %v", got.TokenIDs, secondBlock)
	}
}

func TestARemovedEventNamesTheBlocksLeaving(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "removed.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := batch.Events[0]
	if got.Kind != kvevents.Removed {
		t.Errorf("kind = %v, want %v", got.Kind, kvevents.Removed)
	}
	if want := []uint64{0x8000000000000001}; !slices.Equal(got.BlockHashes, want) {
		t.Errorf("block hashes = %#x, want %#x", got.BlockHashes, want)
	}
	if got.Medium != "GPU" {
		t.Errorf("medium = %q, want GPU", got.Medium)
	}
}

func TestAClearedEventIsTheWholeCacheAtOnce(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "cleared.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(batch.Events) != 1 || batch.Events[0].Kind != kvevents.Cleared {
		t.Fatalf("events = %+v, want one %v", batch.Events, kvevents.Cleared)
	}
	if len(batch.Events[0].BlockHashes) != 0 {
		t.Errorf("a clear named blocks %v, but it empties the cache whatever it held", batch.Events[0].BlockHashes)
	}
}

// A replica told to send whole digests rather than their low 64 bits names each
// block by the same 64 bits it would otherwise have sent, so the two
// configurations cannot describe one block as two.
func TestAWholeDigestIsReadAsItsLowSixtyFourBits(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "bytes_hashes.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := batch.Events[0]
	// gen.py's digests are the bytes 0..31 and 100..131; the engine's integer
	// form is the last eight of each, read big-endian.
	if want := []uint64{0x18191a1b1c1d1e1f}; !slices.Equal(got.BlockHashes, want) {
		t.Errorf("block hashes = %#x, want %#x", got.BlockHashes, want)
	}
	if got.Parent == nil || *got.Parent != 0x7c7d7e7f80818283 {
		t.Errorf("parent = %v, want 0x7c7d7e7f80818283", got.Parent)
	}
}

// A block whose hash covers more than its tokens — a LoRA adapter, a cache salt
// — is one no prompt identified by its tokens alone can ever match, however
// alike the tokens are. The decoder reports those facts per block so that the
// index can refuse to believe in such a match rather than invent one.
func TestABlockKeyedBeyondItsTokensIsReportedAsSuch(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "keyed.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := batch.Events[0]
	if !got.LoRA {
		t.Error("blocks stored under a LoRA adapter were not reported as such")
	}
	if want := []bool{true, false}; !slices.Equal(got.ExtraKeys, want) {
		t.Errorf("extra keys per block = %v, want %v: the salt is on the first block only", got.ExtraKeys, want)
	}

	plain, err := kvevents.Decode(golden(t, "stored.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if plain.Events[0].LoRA || slices.Contains(plain.Events[0].ExtraKeys, true) {
		t.Errorf("a plain block was reported as keyed: lora %v, extra keys %v", plain.Events[0].LoRA, plain.Events[0].ExtraKeys)
	}
}

// One engine step can store, remove and clear, and the order is the meaning: a
// block removed after it was stored is gone, and one stored after a clear is
// held.
func TestABatchKeepsItsEventsInTheOrderTheEngineQueuedThem(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "mixed.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var kinds []kvevents.Kind
	for _, e := range batch.Events {
		kinds = append(kinds, e.Kind)
	}
	if want := []kvevents.Kind{kvevents.Stored, kvevents.Stored, kvevents.Removed, kvevents.Cleared}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if batch.Events[1].Medium != "CPU" {
		t.Errorf("the second store's medium = %q, want CPU", batch.Events[1].Medium)
	}
}

// A payload cut short anywhere is an error, never a panic and never a batch
// holding part of its events. The stream treats a batch it cannot read as lost
// history, and a half-read one would apply some of an engine step's events and
// silently drop the rest.
func TestATruncatedPayloadIsAnErrorWhereverItIsCut(t *testing.T) {
	whole := golden(t, "mixed.msgpack")
	for n := range len(whole) {
		if _, err := kvevents.Decode(whole[:n]); err == nil {
			t.Errorf("a payload cut to %d of its %d bytes decoded without error", n, len(whole))
		}
	}
}

func TestBytesAfterTheBatchAreAnError(t *testing.T) {
	payload := append(golden(t, "cleared.msgpack"), 0xc0)
	if _, err := kvevents.Decode(payload); err == nil {
		t.Error("a batch followed by a stray byte decoded without error")
	}
}

// Earlier engines encoded each event as an array rather than a map. A replica
// speaking that encoding is not the pinned engine, and reading it as though it
// were would put its fields in the wrong places without an error.
func TestAnEventThatIsNotAMapIsRefused(t *testing.T) {
	// [1.0, [["BlockRemoved", [1]]], 0]
	payload := []byte{0x93, 0xcb, 0x3f, 0xf0, 0, 0, 0, 0, 0, 0, 0x91, 0x92, 0xac}
	payload = append(payload, "BlockRemoved"...)
	payload = append(payload, 0x91, 0x01, 0x00)
	if _, err := kvevents.Decode(payload); err == nil {
		t.Error("an event encoded as an array decoded without error")
	}
}

// An event type the pinned engine does not have is version skew between the
// router and the replica, and it is reported by name rather than skipped.
func TestAnUnknownEventTypeIsRefusedByName(t *testing.T) {
	// [1.0, [{"type": "BlockMoved"}], 0]
	payload := []byte{0x93, 0xcb, 0x3f, 0xf0, 0, 0, 0, 0, 0, 0, 0x91, 0x81, 0xa4}
	payload = append(payload, "type"...)
	payload = append(payload, 0xaa)
	payload = append(payload, "BlockMoved"...)
	payload = append(payload, 0x00)
	_, err := kvevents.Decode(payload)
	if err == nil || !strings.Contains(err.Error(), "BlockMoved") {
		t.Errorf("an unknown event type gave error %v, want one naming BlockMoved", err)
	}
}

// The rank is optional in the engine's schema. A batch that leaves it empty is
// still a batch, and refusing it would open a gap in the stream over a field
// this fleet has no use for.
func TestABatchWithNoRankStillDecodes(t *testing.T) {
	batch, err := kvevents.Decode(golden(t, "unranked.msgpack"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(batch.Events) != 1 || batch.Events[0].Kind != kvevents.Cleared {
		t.Errorf("events = %+v, want one %v", batch.Events, kvevents.Cleared)
	}
}
