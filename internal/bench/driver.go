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
	"strconv"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
	"github.com/yuchia329/kvroute/internal/session"
)

// SessionHeader carries the session identity to the router.
//
// Aliased rather than spelled out again: the router reads this header to decide
// which conversation a request belongs to, and a second copy of the string here
// could drift from it without any test failing — the harness would simply stop
// telling the router what it knows, and every session-aware policy would fall
// back to deriving an identity while the run still called itself header-routed.
const SessionHeader = session.Header

// Driver names which load generator produced a row or a cell, so no table can be
// read without knowing which one produced it.
//
// A named type rather than a bare string, as record.Outcome is: it is recorded
// in every row, it is what tells two rows apart that are otherwise identical,
// and the set of values is closed.
type Driver string

const (
	// ClosedLoopDriver holds a fixed number of virtual users. It throttles
	// itself when the fleet slows, so its tail is systematically optimistic;
	// the headline goodput number comes from the open-loop driver.
	ClosedLoopDriver Driver = "closed_loop"
	// OpenLoopDriver fires on a fixed arrival schedule. Offered load is its
	// input rather than its outcome, which is what makes it the one the
	// headline goodput number comes from.
	OpenLoopDriver Driver = "open_loop"
)

// Name is how a driver reads in prose and in a table. The recorded value is the
// machine-readable one; this is the same fact spelled for a reader.
func (d Driver) Name() string {
	switch d {
	case ClosedLoopDriver:
		return "closed-loop"
	case OpenLoopDriver:
		return "open-loop"
	case "":
		// A cell recorded before cells named their driver. Better an admission
		// than a guess: this is the column the table exists to be honest about.
		return "**unstated**"
	default:
		return string(d)
	}
}

// DriverConfig configures one cell, under either driver.
//
// One type rather than two because the drivers differ in exactly one field:
// closed-loop holds a Concurrency and open-loop fires at an ArrivalRate.
// Everything else — what is sent, where, for how long, and how the outcome is
// booked — has to be identical or the two drivers' rows could not populate one
// table.
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
	// Concurrency is the number of virtual users held for the whole cell. It is
	// RunClosedLoop's input, and RunOpenLoop ignores it.
	Concurrency int
	// ArrivalRate is requests per second. It is RunOpenLoop's input — the
	// schedule it fires on whether or not earlier requests have finished — and
	// RunClosedLoop ignores it.
	ArrivalRate float64
	// ThinkTime is how long a session waits between its turns, and with
	// ArrivalRate it sets how many conversations an open-loop cell holds open at
	// once. Zero uses DefaultThinkTime. RunClosedLoop ignores it: there a
	// session's next turn is offered when its last response arrives, which is
	// the closed loop's own definition of think time and is zero by
	// construction.
	ThinkTime time.Duration
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
// Driver, Concurrency and ArrivalRate are not inputs: each driver fills in the
// ones it is authoritative for, because a label that could disagree with the run
// is a label that will.
type Labels struct {
	CellID string `json:"cell_id" parquet:"cell_id"`
	Policy string `json:"policy" parquet:"policy"`
	// Driver is which load generator produced the row. Both drivers write the
	// same schema, so the rows of a closed-loop cell and an open-loop one are
	// concatenated into one file and read by one query; this column is what
	// keeps them distinguishable once they are.
	Driver Driver `json:"driver" parquet:"driver"`
	// Concurrency is the number of virtual users held, under the closed-loop
	// driver. Zero under the open-loop one, where concurrency is an outcome.
	Concurrency int `json:"concurrency" parquet:"concurrency"`
	// ArrivalRate is the offered requests per second, under the open-loop
	// driver. Zero under the closed-loop one, where offered load is an outcome.
	ArrivalRate float64 `json:"arrival_rate" parquet:"arrival_rate"`
	Repetition  int     `json:"repetition" parquet:"repetition"`
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
	cfg.Labels.Driver = ClosedLoopDriver
	cfg.Labels.Concurrency = cfg.Concurrency
	// Cleared, not merely left alone: nothing scheduled these requests, and a
	// row carrying a rate no schedule offered is exactly the label that
	// disagrees with its run.
	cfg.Labels.ArrivalRate = 0

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
		// No due time: under this driver the next request is due when the last
		// one finished, so there is no schedule to be late against.
		row := sendTurn(ctx, cfg, user, turn, warmUntil, time.Time{})
		rows = append(rows, row)
		writeRow(cfg, row)
	}
}

// writeRow appends one row to the run's record, if there is one. A failed write
// is logged rather than returned: the returned rows are still whole, and
// abandoning a cell that is otherwise running would cost more than the line that
// was lost.
func writeRow(cfg DriverConfig, row Result) {
	if cfg.Rows == nil {
		return
	}
	if err := cfg.Rows.Write(row); err != nil {
		cfg.Log.Error("could not write a row", "cell", cfg.Labels.CellID, "err", err)
	}
}

// sendTurn sends one request and observes what came back.
//
// scheduled is when an arrival schedule said this request was due, and the zero
// time when nothing scheduled it. Both drivers share this function so that an
// outcome cannot be booked one way under one driver and another way under the
// other.
func sendTurn(ctx context.Context, cfg DriverConfig, user, turn int, warmUntil, scheduled time.Time) Result {
	next := cfg.Workload.Next(user, turn)
	started := time.Now()
	row := Result{
		Labels:      cfg.Labels,
		Session:     next.Session,
		Turn:        next.Index,
		VirtualUser: user,
		PromptBytes: int64(len(next.Body)),
		// Judged on when the request started: a request that began inside the
		// warm-up window is a warm-up request however long it took to finish.
		// One rule for both drivers, so a row's warm-up flag means the same
		// thing wherever it came from.
		Warmup:      started.Before(warmUntil),
		StartedAtNs: started.UnixNano(),
	}
	if !scheduled.IsZero() {
		row.ScheduledAtNs = scheduled.UnixNano()
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
	// Absent under a policy that consults no index, and absent entirely when a
	// replica is being driven directly. Both are honestly zero: no prefix match
	// was predicted because nothing predicted one.
	row.PrefixMatchBytes, _ = strconv.Atoi(resp.Header.Get(router.PrefixMatchHeader))
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
	// The engine's own account of the prompt, read back off the response the
	// router passed through untouched. It is the ground truth the prefix match on
	// this same row predicted, and putting the two side by side is all belief
	// divergence costs.
	row.EnginePromptTokens = s.usage.promptTokens
	row.EngineCachedTokens = s.usage.cachedTokens
	row.EngineUsageRead = s.usage.read
	row.EngineCacheRead = s.usage.cacheRead
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
