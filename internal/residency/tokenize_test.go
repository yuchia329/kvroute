package residency_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/residency"
)

const chatBody = `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}],"stream":true,"max_tokens":8}`

func engine(t *testing.T, handler http.HandlerFunc) fleet.Replica {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return fleet.Replica{ID: "replica-0", BaseURL: srv.URL}
}

// The engine renders the chat template and tokenizes the result with its own
// code, so the tokens it returns are the ones a chat completion of the same body
// would compute. That only holds if it is sent the same body: every field the
// template reads has to reach it, which is why the body goes verbatim rather
// than rebuilt from the few fields the router knows about.
func TestTheEngineTokenizesTheChatBodyItWasSent(t *testing.T) {
	var path, contentType string
	var sent []byte
	replica := engine(t, func(w http.ResponseWriter, r *http.Request) {
		path, contentType = r.URL.Path, r.Header.Get("Content-Type")
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"count":3,"max_model_len":8192,"tokens":[128000,9906,1917],"token_strs":null}`)
	})

	got, err := (&residency.EngineTokenizer{}).Tokenize(context.Background(), replica, []byte(chatBody))
	if err != nil {
		t.Fatalf("Tokenize: %v", err)
	}
	if want := []uint32{128000, 9906, 1917}; !slices.Equal(got, want) {
		t.Errorf("tokens = %v, want %v", got, want)
	}
	if path != "/tokenize" {
		t.Errorf("asked %s, want /tokenize", path)
	}
	if contentType != "application/json" {
		t.Errorf("content type %q, want application/json: the engine refuses anything else", contentType)
	}
	if !bytes.Equal(sent, []byte(chatBody)) {
		t.Errorf("sent\n  %s\nwant the chat body verbatim\n  %s", sent, chatBody)
	}
}

// An engine that refuses the request says why, and the status is what tells a
// malformed body from an engine that is not a vLLM at all.
func TestAnEngineRefusalIsReportedWithItsStatus(t *testing.T) {
	replica := engine(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"messages is required","code":400}}`)
	})

	_, err := (&residency.EngineTokenizer{}).Tokenize(context.Background(), replica, []byte(chatBody))
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("a refused tokenization gave error %v, want one naming status 400", err)
	}
}

// Tokenizing is on the request path: the request waits for it before it is
// routed at all. A tokenization slower than its budget is abandoned, so that the
// request can be routed on load instead of waiting on an API server that has
// stopped answering.
func TestATokenizationSlowerThanItsBudgetIsAbandoned(t *testing.T) {
	release := make(chan struct{})
	replica := engine(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	// Registered after the server, so it runs before the server's Close: that
	// waits for every handler to return, and this one returns only when
	// released.
	t.Cleanup(func() { close(release) })

	started := time.Now()
	_, err := (&residency.EngineTokenizer{Timeout: 50 * time.Millisecond}).Tokenize(context.Background(), replica, []byte(chatBody))
	if err == nil {
		t.Fatal("a tokenization that never answered returned no error")
	}
	if waited := time.Since(started); waited > time.Second {
		t.Errorf("waited %v on a tokenization with a 50ms budget", waited)
	}
}
