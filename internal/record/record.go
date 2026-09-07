// Package record writes the per-request rows that are this project's system of
// record.
//
// Prometheus is a sampled TSDB and the wrong shape for per-request tail
// latency across hundreds of cells, so every reported figure is instead
// recomputable from these rows. They are written as JSONL during a run, one
// line flushed per request, so that a crashed sweep still leaves readable
// partial data.
package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Outcome is how a request ended. Dropped, failed and SLO violation are kept
// distinct: folding fast failures into a latency distribution makes an
// overloaded fleet look faster. SLO violation is derived during analysis, from
// the timings on a successful row.
type Outcome string

const (
	// OutcomeSuccess is a complete response from a replica.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailed is a request a replica accepted and then errored on.
	OutcomeFailed Outcome = "failed"
	// OutcomeDropped is a request the router could not place at all.
	OutcomeDropped Outcome = "dropped"
	// OutcomeCancelled is a request whose client went away before the response
	// finished. It is neither dropped nor failed: no replica errored and the
	// router placed it fine. It has its own value so that a disconnect cannot
	// inflate the failure count, which is the one number an overloaded fleet
	// must not be able to hide behind.
	//
	// CONTEXT.md does not name this outcome yet; it needs a domain-modeling
	// pass rather than a silent addition to the glossary.
	OutcomeCancelled Outcome = "cancelled"
)

// Request is one row: everything observed about a single request through the
// router.
type Request struct {
	RequestID string    `json:"request_id"`
	StartedAt time.Time `json:"started_at"`

	Policy         string `json:"policy"`
	Replica        string `json:"replica"`
	DecisionReason string `json:"decision_reason"`

	Model  string `json:"model"`
	Stream bool   `json:"stream"`

	// RouterOverheadNs is accept to upstream dispatch: the router's own cost,
	// reported separately so it is never hidden inside TTFT.
	RouterOverheadNs int64 `json:"router_overhead_ns"`
	// TTFTNs is client-observed time to first response byte. Zero when no byte
	// ever reached the client.
	TTFTNs int64 `json:"ttft_ns"`
	// TotalNs is accept to last byte written to the client.
	TotalNs int64 `json:"total_ns"`

	UpstreamStatus int     `json:"upstream_status"`
	ResponseBytes  int64   `json:"response_bytes"`
	Outcome        Outcome `json:"outcome"`
	Error          string  `json:"error,omitempty"`
}

// Writer appends rows as JSONL. It is safe for concurrent use.
type Writer struct {
	mu  sync.Mutex
	buf *bufio.Writer
	c   io.Closer
}

// Open appends rows to path, creating it if needed. An empty path discards
// rows, so a router can run without a record file.
func Open(path string) (*Writer, error) {
	if path == "" {
		return NewWriter(io.Discard), nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("record: open %s: %w", path, err)
	}
	w := NewWriter(f)
	w.c = f
	return w, nil
}

// NewWriter writes rows to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{buf: bufio.NewWriter(w)}
}

// Write appends one row and flushes it, so a crash loses at most the row in
// flight.
func (w *Writer) Write(r Request) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("record: marshal: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.buf.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("record: write: %w", err)
	}
	return w.buf.Flush()
}

// Close flushes and closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		return err
	}
	if w.c != nil {
		return w.c.Close()
	}
	return nil
}
