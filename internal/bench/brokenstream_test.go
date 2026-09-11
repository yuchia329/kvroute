package bench_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// placedBy stands in for a router that has placed a request: it names the
// replica, starts an event stream, and then does whatever the test says to the
// body. The harness is what is under test, so the stream is written by hand
// rather than produced by a replica that would always finish it.
func placedBy(t *testing.T, body func(w http.ResponseWriter, flush func())) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set(router.ReplicaHeader, "replica-0")
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		body(w, func() { _ = http.NewResponseController(w).Flush() })
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// firstToken is one content chunk of a stream, as the engine writes it.
const firstToken = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" t0\"}}]}\n\n"

// onlyRequest sends exactly one request and returns its row: a closed-loop cell
// of one user whose window closes before its second turn.
func onlyRequest(t *testing.T, target string) bench.Result {
	t.Helper()
	rows := drive(t, bench.DriverConfig{Target: target, Concurrency: 1, Duration: time.Nanosecond})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want exactly one", len(rows))
	}
	return rows[0]
}

// A stream that breaks after it has begun is a request whose replica was lost
// under it: the router placed it, the replica started answering, and the client
// never received a complete response. CONTEXT.md counts that as dropped, and
// idea.md §7 is explicit that it must not be passed off as anything gentler —
// it is the request no reroute can save, and the chaos test's drop count is
// made of exactly these.
func TestAStreamBrokenMidResponseIsDropped(t *testing.T) {
	target := placedBy(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, firstToken)
		flush()
		// What the router does when it loses the replica mid-stream: abort the
		// connection rather than end the response, so the client cannot mistake
		// a cut-off stream for a finished one.
		panic(http.ErrAbortHandler)
	})

	row := onlyRequest(t, target)

	if row.Outcome != record.OutcomeDropped {
		t.Errorf("outcome = %q, want %q: the client never received a complete response", row.Outcome, record.OutcomeDropped)
	}
	if row.TTFTNs <= 0 {
		t.Errorf("ttft_ns = %d, but the first token did arrive before the stream broke", row.TTFTNs)
	}
	if row.Error == "" {
		t.Error("the dropped row carries no error saying what happened")
	}
}

// A stream that simply stops, without the terminator every engine response ends
// with, did not finish either. It is the same loss as above arriving by a
// quieter path — a router that ends the response instead of aborting it when its
// replica goes, which is exactly what this one did before it learned to abort —
// and booked as a success it would put a truncated answer in the success column
// and its latency in the percentiles.
func TestAStreamThatStopsWithoutItsTerminatorIsNotASuccess(t *testing.T) {
	target := placedBy(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, firstToken)
		flush()
	})

	row := onlyRequest(t, target)

	if row.Outcome != record.OutcomeDropped {
		t.Errorf("outcome = %q, want %q: the stream ended before its [DONE]", row.Outcome, record.OutcomeDropped)
	}
	if !strings.Contains(row.Error, "[DONE]") {
		t.Errorf("error = %q, want it to say the terminator never came", row.Error)
	}
}
