package session_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/session"
)

// conversation renders turn n of a conversation the way the multi-turn
// generator does: the same opening messages every turn, with the history of
// prior turns appended. That resend is what makes a derived identity stable, so
// the fixture has to reproduce it rather than assert it.
func conversation(system, opening string, turns int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"m","messages":[`)
	if system != "" {
		fmt.Fprintf(&b, `{"role":"system","content":%q},`, system)
	}
	fmt.Fprintf(&b, `{"role":"user","content":%q}`, opening)
	for turn := 1; turn < turns; turn++ {
		fmt.Fprintf(&b, `,{"role":"assistant","content":"reply %d"}`, turn)
		fmt.Fprintf(&b, `,{"role":"user","content":"follow-up %d"}`, turn)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func header(id string) http.Header {
	h := http.Header{}
	h.Set(session.Header, id)
	return h
}

func TestTheHeaderIsTheSessionIdentity(t *testing.T) {
	got := session.Identify(header("chat-42"), conversation("you are a bot", "hello", 1))

	if got.ID != "chat-42" {
		t.Errorf("identity is %q, want chat-42: the client supplied it", got.ID)
	}
	if got.Derived {
		t.Error("identity is marked derived, but the client supplied it")
	}
	if !got.Known() {
		t.Error("a supplied identity is not known")
	}
}

// The fallback exists so the router needs no client cooperation, and it is only
// worth anything if it survives the conversation growing. Turn N resends turns
// 1..N-1, so the opening messages are the same bytes on every turn — which is
// exactly what is hashed.
func TestWithoutTheHeaderIdentityIsDerivedFromTheOpeningAndIsStableAcrossTurns(t *testing.T) {
	first := session.Identify(nil, conversation("you are a bot", "hello", 1))
	if !first.Known() {
		t.Fatal("a conversation with a system and a user message derived no identity")
	}
	if !first.Derived {
		t.Error("identity is not marked derived, but no header supplied it")
	}

	for _, turn := range []int{2, 5, 20} {
		later := session.Identify(nil, conversation("you are a bot", "hello", turn))
		if later.ID != first.ID {
			t.Errorf("turn %d derived %q against the first turn's %q, so the identity does not survive the history growing",
				turn, later.ID, first.ID)
		}
	}
}

// Two conversations that open differently are two sessions. Without this the
// fallback would pin the whole workload to one replica and the policy built on
// it would be measuring nothing.
func TestConversationsThatOpenDifferentlyDeriveDifferentIdentities(t *testing.T) {
	seen := map[string]string{}
	for _, opening := range []string{"hello", "hi", "what is a KV cache", "hello "} {
		got := session.Identify(nil, conversation("you are a bot", opening, 3))
		if other, clash := seen[got.ID]; clash {
			t.Errorf("%q and %q derived the same identity %q", other, opening, got.ID)
		}
		seen[got.ID] = opening
	}

	// The system prompt is part of the identity too: a shared system prompt is
	// the cross-session prefix the workload deliberately contains, and two
	// sessions differing only in it are still two sessions.
	plain := session.Identify(nil, conversation("", "hello", 1))
	prompted := session.Identify(nil, conversation("you are a bot", "hello", 1))
	if plain.ID == prompted.ID {
		t.Errorf("a conversation with a system prompt and one without derived the same identity %q", plain.ID)
	}
}

// An empty header is no header rather than an empty session: a client that sends
// the header blank has told the router nothing, and treating "" as an identity
// would pin every such request to whichever replica the empty string hashes to.
func TestAnEmptyHeaderIsNoHeader(t *testing.T) {
	body := conversation("you are a bot", "hello", 1)

	blank := session.Identify(header("   "), body)
	if !blank.Derived {
		t.Errorf("a blank header was taken as the identity %q", blank.ID)
	}
	if want := session.Identify(nil, body); blank.ID != want.ID {
		t.Errorf("a blank header derived %q, but no header at all derives %q", blank.ID, want.ID)
	}
}

// A request with nothing stable to hash has no identity, and says so. The
// alternative is hashing the whole body, which changes on every turn: that would
// hand the affinity policy a fresh session per turn and quietly turn it into a
// worse round-robin, with nothing in the record admitting it.
func TestARequestWithNoOpeningMessagesHasNoIdentity(t *testing.T) {
	for _, body := range []string{
		`not json at all`,
		`{"model":"m"}`,
		`{"model":"m","messages":[]}`,
		`{"model":"m","messages":[{"role":"assistant","content":"orphan"}]}`,
	} {
		got := session.Identify(nil, []byte(body))
		if got.Known() {
			t.Errorf("body %s derived the identity %q, but it carries no opening message to derive one from", body, got.ID)
		}
	}
}

// A system prompt is not a conversation. The workload deliberately gives a
// configurable fraction of sessions one shared system prompt, so deriving an
// identity from it alone would collapse all of those onto one replica — and
// idea.md §5 is explicit that sticky hashing scattering them evenly is the right
// behaviour and concentrating them would be worse.
func TestASystemPromptAloneIsNotAConversationToIdentify(t *testing.T) {
	systemOnly := []byte(`{"model":"m","messages":[{"role":"system","content":"you are a bot"}]}`)

	if got := session.Identify(nil, systemOnly); got.Known() {
		t.Errorf("a request carrying only a shared system prompt derived the identity %q", got.ID)
	}

	// With a user message it identifies normally, and the system prompt is part
	// of that identity rather than ignored.
	withUser := session.Identify(nil, conversation("you are a bot", "hello", 1))
	if !withUser.Known() {
		t.Error("a system prompt followed by a user message derived no identity")
	}
}

// Content is hashed as the bytes the client sent rather than as a string, so a
// multimodal content array identifies its session like any other.
func TestAContentArrayIdentifiesItsSessionLikeAnyOtherOpening(t *testing.T) {
	array := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	other := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"goodbye"}]}]}`)

	got, differing := session.Identify(nil, array), session.Identify(nil, other)
	if !got.Known() {
		t.Fatal("a content array derived no identity")
	}
	if got.ID == differing.ID {
		t.Error("two different content arrays derived one identity")
	}
}
