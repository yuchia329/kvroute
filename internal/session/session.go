// Package session resolves which conversation a request belongs to.
//
// The session is the unit cache locality is preserved for, so every policy that
// routes on conversation rather than on load starts here. It is its own package
// because two callers need the same answer for different reasons: the router
// records the identity on the row, and a policy routes on it, and an identity
// that differed between those two would make the record unable to explain the
// routing.
//
// Both paths idea.md §4.2 calls for ship: the header a real gateway is handed,
// and a fallback derived from the conversation's opening, which needs no client
// cooperation. The fallback is not only a fallback — it is the derived-key
// policy in its own right, so the sensitivity check of routing on a key the
// client never sent comes free with it.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// Header is what a client supplies its session identity in. The OpenAI wire
// format carries no conversation id, so a gateway in front of a real chat
// product is handed one exactly like this.
const Header = "X-Session-Id"

// Session is which conversation a request belongs to, and where that answer
// came from.
type Session struct {
	// ID is the conversation's identity. Empty when the request carries nothing
	// stable enough to identify one.
	ID string
	// Derived is true when the id was computed from the request's opening
	// messages rather than supplied by the client.
	//
	// It is recorded rather than inferred from the id's shape because the two
	// paths are different experiments: routing on a key the client supplied is
	// the oracle idea.md §5 grants policy 3, and routing on a derived key is
	// what the same policy is worth without one. A run that could not say which
	// it had could not tell those apart.
	Derived bool
}

// Known reports whether the request identified a conversation at all.
func (s Session) Known() bool { return s.ID != "" }

// Identify resolves the session a request belongs to: the header when the client
// supplied one, and otherwise a hash of the conversation's opening.
//
// A whitespace-only header is treated as absent. A client that sends the header
// blank has told the router nothing, and taking "" as an identity would pin
// every such request to whichever replica the empty string happens to hash to.
func Identify(h http.Header, body []byte) Session {
	if id := strings.TrimSpace(h.Get(Header)); id != "" {
		return Session{ID: id}
	}
	if id, ok := derive(body); ok {
		return Session{ID: id, Derived: true}
	}
	return Session{}
}

// chatBody is the little of a chat completions request an identity is derived
// from. Content is kept as raw bytes rather than decoded, so a string content
// and a multimodal content array are both just the bytes the client sent, and
// neither needs a shape here to be hashed.
type chatBody struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// derive hashes the conversation's opening: its first system message and its
// first user message.
//
// The opening rather than the whole body, because turn N resends turns 1..N-1
// and only the opening is the same bytes on every turn of one conversation.
// Hashing the whole body would produce a fresh identity per turn, which would
// leave a session-affinity policy scattering the turns of one conversation
// across the fleet while still reporting itself as session affinity — a failure
// with no symptom in the record.
//
// Two conversations that open identically derive one identity, and that is the
// intended reading rather than a collision: sessions branching from a common
// ancestor share their opening, and the cache locality they share is real.
//
// Whether a request that supplies no header would be better served by no
// identity at all is a judgement the caller makes, which is why this reports
// whether it found one instead of inventing a key from the request's bytes.
func derive(body []byte) (string, bool) {
	var parsed chatBody
	// A body the router cannot parse is still forwarded upstream — deciding
	// whether it is valid is the replica's job — so a parse failure here is a
	// request with no identity, not a request that is rejected.
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}

	sum := sha256.New()
	// The user message is what makes this a conversation, so it is required and
	// the system prompt is only folded in when there is one.
	//
	// A system prompt alone must not identify anything. The workload gives a
	// configurable fraction of its sessions one shared system prompt, and idea.md
	// §5 is explicit that scattering those evenly is the right behaviour and that
	// concentrating them would be worse — so an identity derived from the shared
	// prompt alone would collapse every one of them onto a single replica, which
	// is the opposite of what this policy is supposed to do.
	seen := false
	for _, want := range []string{"system", "user"} {
		for _, m := range parsed.Messages {
			if m.Role != want {
				continue
			}
			// The role is written in beside the content so that a system
			// message and a user message carrying the same text cannot swap
			// places without changing the identity.
			sum.Write([]byte(want))
			sum.Write([]byte{0})
			sum.Write(m.Content)
			sum.Write([]byte{0})
			if want == "user" {
				seen = true
			}
			break
		}
	}
	if !seen {
		return "", false
	}
	// Truncated to 128 bits: this is an identity in a record and a key on a hash
	// ring, not a digest anything is authenticated against, and a full SHA-256
	// would double the width of the column for no reading anybody makes of it.
	return "derived-" + hex.EncodeToString(sum.Sum(nil))[:32], true
}
