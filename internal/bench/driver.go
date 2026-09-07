package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// SessionHeader carries the session identity to the router.
const SessionHeader = "X-Session-Id"

// DriverConfig configures one closed-loop cell.
type DriverConfig struct {
	// Target is the base URL to drive: the router, or one replica directly when
	// DirectReplica names it.
	Target string
	// DirectReplica, when set, says Target is a replica rather than the router,
	// and names it. Rows are then attributed to it directly, because there is
	// no router in the path to name it in a response header.
	//
	// This exists for the characterization pass, which drives each replica on
	// its own to establish the hardware latency floor and to check that the six
	// are interchangeable. Both questions are about a replica, and putting the
	// router in the path would fold its policy — and its overhead — into the
	// answer.
	DirectReplica string
	// Concurrency is the number of virtual users held for the whole cell.
	Concurrency int
	// Duration is how long users keep starting new turns. A request already in
	// flight when it expires is allowed to finish.
	Duration time.Duration
	// Warmup is how long at the start of the cell counts as warm-up. Rows
	// started inside it are kept and excluded from the summary.
	//
	// A duration rather than a count per virtual user. A count costs
	// count x per-request latency, and per-request latency grows with
	// concurrency, so the saturated cells — the ones the sweep exists to
	// measure — would forfeit the largest share of their window. A duration
	// costs every level the same.
	Warmup time.Duration

	Workload Workload
	// Rows, when set, receives every row as it completes, so a crashed cell
	// still leaves readable partial data.
	Rows *record.Writer[Result]

	// Labels are stamped onto every row so a row can be traced back to the cell
	// that produced it after the files are concatenated.
	Labels Labels

	Client *http.Client
	Log    *slog.Logger
}

// Labels identify the cell a row belongs to. They are stamped onto every row
// and embedded in Result, so a row and its cell cannot disagree about which
// cell it is.
//
// Concurrency is not an input: RunClosedLoop fills it from the concurrency it
// was actually asked to hold, because a label that could disagree with the run
// is a label that will.
type Labels struct {
	CellID      string `json:"cell_id" parquet:"cell_id"`
	Policy      string `json:"policy" parquet:"policy"`
	Concurrency int    `json:"concurrency" parquet:"concurrency"`
	Repetition  int    `json:"repetition" parquet:"repetition"`
}

// DefaultClient dispatches to the router.
//
// It mirrors the router's own upstream client: compression off so response
// bytes are the replica's bytes, and an idle connection pool large enough for
// the top of the concurrency sweep. At 256 virtual users the standard library's
// default of two idle connections per host would have the driver measuring TCP
// setup as though it were inference.
func DefaultClient(concurrency int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.MaxIdleConns = 2 * concurrency
	transport.MaxIdleConnsPerHost = 2 * concurrency
	// No client timeout: a streaming response is long-lived by design, and
	// cutting one at an arbitrary deadline would manufacture failures that the
	// fleet did not produce.
	return &http.Client{Transport: transport}
}

// RunClosedLoop runs one cell and returns its rows.
//
// A closed-loop driver holds a fixed number of virtual users, each sending its
// next request only after the previous response completes, so offered load is
// an outcome rather than an input. That makes it the right driver for the
// concurrency axis and the wrong one for the headline goodput number: when the
// fleet slows, a closed-loop driver slows with it and never pushes past the
// knee, so its tail is systematically optimistic. The open-loop driver exists
// for that, and every table says which produced it.
func RunClosedLoop(ctx context.Context, cfg DriverConfig) ([]Result, error) {
	if cfg.Target == "" {
		return nil, errors.New("bench: a target router URL is required")
	}
	if cfg.Concurrency <= 0 {
		return nil, fmt.Errorf("bench: concurrency must be positive, got %d", cfg.Concurrency)
	}
	if cfg.Workload == nil {
		return nil, errors.New("bench: a workload is required")
	}
	if cfg.Client == nil {
		cfg.Client = DefaultClient(cfg.Concurrency)
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Warmup >= cfg.Duration && cfg.Duration > 0 {
		return nil, fmt.Errorf("bench: warm-up %v leaves nothing of a %v cell to measure", cfg.Warmup, cfg.Duration)
	}
	cfg.Labels.Concurrency = cfg.Concurrency

	start := time.Now()
	deadline := start.Add(cfg.Duration)
	warmUntil := start.Add(cfg.Warmup)
	collected := make([][]Result, cfg.Concurrency)

	var wg sync.WaitGroup
	for user := range cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			collected[user] = runVirtualUser(ctx, cfg, user, deadline, warmUntil)
		}()
	}
	wg.Wait()

	var results []Result
	for _, rows := range collected {
		results = append(results, rows...)
	}
	// Ordered by start so the rows read as the cell ran rather than as the
	// goroutines happened to be scheduled.
	sort.SliceStable(results, func(i, j int) bool { return results[i].StartedAtNs < results[j].StartedAtNs })
	return results, nil
}

// runVirtualUser sends turns one at a time until the deadline. It checks the
// deadline before starting a turn and never during one: cutting a response
// short at the driver's own clock would put the driver in the failure column.
func runVirtualUser(ctx context.Context, cfg DriverConfig, user int, deadline, warmUntil time.Time) []Result {
	var rows []Result
	for turn := 0; ; turn++ {
		if turn > 0 && (time.Now().After(deadline) || ctx.Err() != nil) {
			return rows
		}
		row := sendTurn(ctx, cfg, user, turn, warmUntil)
		rows = append(rows, row)
		if cfg.Rows != nil {
			if err := cfg.Rows.Write(row); err != nil {
				cfg.Log.Error("could not write a row", "cell", cfg.Labels.CellID, "err", err)
			}
		}
	}
}

// sendTurn sends one request and observes what came back.
func sendTurn(ctx context.Context, cfg DriverConfig, user, turn int, warmUntil time.Time) Result {
	next := cfg.Workload.Next(user, turn)
	started := time.Now()
	row := Result{
		Labels:      cfg.Labels,
		Session:     next.Session,
		Turn:        turn,
		VirtualUser: user,
		// Judged on when the request started: a request that began inside the
		// warm-up window is a warm-up request however long it took to finish.
		Warmup:      started.Before(warmUntil),
		StartedAtNs: started.UnixNano(),
	}
	finish := func(outcome record.Outcome, err error) Result {
		row.TotalNs = time.Since(started).Nanoseconds()
		row.Outcome = outcome
		if err != nil {
			row.Error = err.Error()
		}
		return row
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Target+router.ChatCompletionsPath, bytes.NewReader(next.Body))
	if err != nil {
		return finish(record.OutcomeDropped, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(SessionHeader, next.Session)

	resp, err := cfg.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return finish(record.OutcomeCancelled, err)
		}
		// Nothing answered, so nothing was placed. Driving a replica directly
		// this is the harness failing to reach it rather than a router failing
		// to place it, but it is the same column either way: no response
		// arrived, so there is no latency to contribute and nothing a replica
		// can be blamed for erroring on.
		return finish(record.OutcomeDropped, err)
	}
	defer resp.Body.Close()

	row.Status = resp.StatusCode
	row.Replica = resp.Header.Get(router.ReplicaHeader)
	row.Decision = resp.Header.Get(router.DecisionHeader)
	row.RequestID = resp.Header.Get(router.RequestHeader)
	if cfg.DirectReplica != "" {
		// Driving a replica directly, so the harness knows where the request
		// went without being told: it chose. Attributing it here rather than
		// leaving the field empty keeps the check below meaning what it says.
		row.Replica = cfg.DirectReplica
	}

	// The router names the replica only once it has dispatched, so the absence
	// of that header is exactly the distinction CONTEXT.md draws: a request the
	// router could not place is dropped, and one a replica accepted and then
	// errored on is failed. Discriminating on the status code instead would put
	// both in the same column.
	if row.Replica == "" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		row.ResponseBytes = int64(len(body))
		return finish(record.OutcomeDropped, fmt.Errorf("router did not place the request: status %d: %s", resp.StatusCode, bytes.TrimSpace(body)))
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		row.ResponseBytes = int64(len(body))
		return finish(record.OutcomeFailed, fmt.Errorf("replica %s returned status %d: %s", row.Replica, resp.StatusCode, bytes.TrimSpace(body)))
	}

	s, readErr := readStream(resp.Body)
	row.ResponseBytes = s.bytes
	row.OutputTokens = s.tokens
	row.ITLMeanNs = s.itlMean.Nanoseconds()
	row.ITLP50Ns = s.itlP50.Nanoseconds()
	row.ITLMaxNs = s.itlMax.Nanoseconds()
	if !s.firstByte.IsZero() {
		row.TTFTNs = s.firstByte.Sub(started).Nanoseconds()
	}
	if readErr != nil {
		if ctx.Err() != nil {
			return finish(record.OutcomeCancelled, readErr)
		}
		// The replica accepted the request and the exchange then broke.
		return finish(record.OutcomeFailed, readErr)
	}
	return finish(record.OutcomeSuccess, nil)
}
