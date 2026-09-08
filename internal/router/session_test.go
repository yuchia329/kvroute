package router_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/session"
)

// postAs sends a request carrying a session header, which is the whole of what a
// real gateway in front of a chat product is handed.
func postAs(t *testing.T, baseURL, sessionID, body string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(session.Header, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	return response{status: resp.StatusCode, replica: resp.Header.Get("X-Kvroute-Replica")}
}

// turn renders turn n of one conversation the way the multi-turn generator does:
// the same opening every turn, with the prior turns appended.
func turn(opening string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":%q}`, opening)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, `,{"role":"assistant","content":"reply %d"},{"role":"user","content":"follow-up %d"}`, i, i)
	}
	b.WriteString(`],"stream":false}`)
	return b.String()
}

// The row has to say which conversation a request belonged to, or nothing
// downstream can check that a session-affinity policy kept one together. The
// harness sends the header and the router is what puts it in the record.
func TestTheRowNamesTheSessionTheRequestBelongedTo(t *testing.T) {
	_, replica := startFake(t, fakereplica.Config{})
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+replica)

	postAs(t, r.url, "chat-9", blockingRequest)

	row := r.rows.wait(t, 1)[0]
	if row.Session != "chat-9" {
		t.Errorf("row names session %q, want chat-9", row.Session)
	}
	if row.SessionDerived {
		t.Error("row marks the session as derived, but the client supplied the header")
	}
}

// The fallback path has to be visible in the record as the fallback path. Both
// ship (idea.md §4.2), and the derived one is a different experiment — routing
// on a key the client never sent — so a run that could not tell them apart could
// not report which it had measured.
func TestARowSaysWhenTheSessionWasDerivedRatherThanSupplied(t *testing.T) {
	_, replica := startFake(t, fakereplica.Config{})
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+replica)

	postAs(t, r.url, "", blockingRequest)

	row := r.rows.wait(t, 1)[0]
	if !row.SessionDerived {
		t.Error("no header was sent, but the row does not mark the session as derived")
	}
	if row.Session == "" {
		t.Error("no session was recorded for a request the router could have derived one from")
	}
	if want := session.Identify(nil, []byte(blockingRequest)); row.Session != want.ID {
		t.Errorf("row derived %q, but the identity of that body is %q", row.Session, want.ID)
	}
}

// The criterion, end to end: every turn of one conversation reaches one replica,
// so that turn N finds turns 1..N-1 in that replica's KV cache. Under
// round-robin these same twelve requests would visit all six replicas twice.
func TestSessionAffinityKeepsAConversationOnOneReplica(t *testing.T) {
	var specs []string
	for i := range 6 {
		_, url := startFake(t, fakereplica.Config{})
		specs = append(specs, fmt.Sprintf("replica-%d=%s", i, url))
	}
	r := startRouterWith(t, policy.NewSessionAffinity(), specs...)

	first := postAs(t, r.url, "chat-9", turn("hello", 1)).replica
	if first == "" {
		t.Fatal("the router did not name the replica it routed to")
	}
	for n := 2; n <= 12; n++ {
		if got := postAs(t, r.url, "chat-9", turn("hello", n)).replica; got != first {
			t.Fatalf("turn %d went to %s, but the conversation started on %s", n, got, first)
		}
	}

	rows := r.rows.wait(t, 12)
	for _, row := range rows {
		if row.DecisionReason != string(policy.ReasonSessionAffinity) {
			t.Errorf("row records decision %q, want %q", row.DecisionReason, policy.ReasonSessionAffinity)
		}
	}
}

// The same conversation with no header at all, which is the path a client that
// cannot be changed takes. The identity is derived from an opening that every
// turn resends, so the conversation stays together without the client's help.
func TestADerivedIdentityKeepsAConversationTogetherWithoutTheClientsHelp(t *testing.T) {
	var specs []string
	for i := range 6 {
		_, url := startFake(t, fakereplica.Config{})
		specs = append(specs, fmt.Sprintf("replica-%d=%s", i, url))
	}
	r := startRouterWith(t, policy.NewSessionAffinity(), specs...)

	first := postAs(t, r.url, "", turn("what is a KV cache", 1)).replica
	for n := 2; n <= 12; n++ {
		if got := postAs(t, r.url, "", turn("what is a KV cache", n)).replica; got != first {
			t.Fatalf("turn %d of a derived session went to %s, but it started on %s", n, got, first)
		}
	}

	// Different conversations still separate: an identity that collapsed every
	// request onto one key would pass the loop above and measure one GPU.
	elsewhere := 0
	for i := range 20 {
		if postAs(t, r.url, "", turn(fmt.Sprintf("unrelated question %d", i), 1)).replica != first {
			elsewhere++
		}
	}
	if elsewhere == 0 {
		t.Error("twenty unrelated conversations all derived onto the replica the first one was on")
	}
}
