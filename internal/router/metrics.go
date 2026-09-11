package router

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/record"
)

// metricsContentType is the Prometheus text exposition format.
//
// Stated exactly, because Prometheus 3 refuses a scrape whose format it cannot
// identify rather than guessing at it. A wrong header is not a warning in a log
// nobody reads: it is a dashboard that stays empty for the whole run.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// metrics is the router's live view of itself, served at GET /metrics so a run
// can be watched while it happens.
//
// It is for watching, never for reporting. The rows are the system of record
// (ADR-0002), and what is counted here is counted from the finished row rather
// than at points of its own in the handler, so the live view and the record
// cannot describe two different sets of requests. Inflight is the one figure
// not taken from rows: it is the fleet's own exact count, read at the moment of
// the scrape, because a row only says what inflight was when its decision was
// made. It is read for every member of the fleet, in rotation or not. A replica
// out of rotation is absent from the snapshot a policy routes from, by design,
// and a drain is watched on exactly that replica's count.
//
// It is pull-only. Nothing here connects out, so a Prometheus that is stopped,
// or was never started, costs a run nothing: the counts go on accumulating and
// nobody reads them. That is what lets a sweep run with the dashboards down.
type metrics struct {
	policy string

	mu        sync.Mutex
	decisions map[string]int64
	overhead  *histogram
	ttft      *histogram
}

func newMetrics(policy string) *metrics {
	return &metrics{
		policy:    policy,
		decisions: map[string]int64{},
		overhead:  newHistogram(overheadBuckets),
		ttft:      newHistogram(ttftBuckets),
	}
}

// observe folds one finished request into the counts.
func (m *metrics) observe(row record.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A request dropped before its policy chose has no decision to count.
	if row.DecisionReason != "" {
		m.decisions[row.DecisionReason]++
	}
	// A request that reached dispatch carries its overhead, whatever happened
	// upstream afterwards; one dropped before dispatch cost the router nothing
	// that ended in a dispatch, and the rows record it as zero.
	if row.RouterOverheadNs > 0 {
		m.overhead.observe(time.Duration(row.RouterOverheadNs))
	}
	// Successes only, as every percentile computed off the rows is. A replica
	// rejecting under overload answers in milliseconds, and its error body has a
	// first byte like any other: folded in, those rejections would make an
	// overloaded fleet look faster.
	if row.Outcome == record.OutcomeSuccess && row.TTFTNs > 0 {
		m.ttft.observe(time.Duration(row.TTFTNs))
	}
}

// write renders the counts in the Prometheus text format, beside each member's
// inflight as it stands.
func (m *metrics) write(w io.Writer, members []fleet.Member) error {
	m.mu.Lock()
	decisions := maps.Clone(m.decisions)
	overhead := m.overhead.snapshot()
	ttft := m.ttft.snapshot()
	m.mu.Unlock()

	var b strings.Builder
	familyHeader(&b, "kvroute_replica_inflight", "gauge",
		"Requests the router has dispatched to this replica and not yet seen complete. Counted exactly, never scraped.")
	for _, member := range members {
		fmt.Fprintf(&b, "kvroute_replica_inflight{replica=%s} %d\n", quoteLabel(member.ID), member.Inflight)
	}
	familyHeader(&b, "kvroute_decisions_total", "counter",
		"Requests by the reason the policy gave for placing them. A rerouted request counts the decision that finally placed it, as its row does.")
	for _, reason := range slices.Sorted(maps.Keys(decisions)) {
		fmt.Fprintf(&b, "kvroute_decisions_total{policy=%s,reason=%s} %d\n", quoteLabel(m.policy), quoteLabel(reason), decisions[reason])
	}
	overhead.render(&b, "kvroute_router_overhead_seconds",
		"Accept to upstream dispatch: the router's own cost, reported apart from TTFT so it is never hidden inside the fleet's latency.")
	ttft.render(&b, "kvroute_ttft_seconds",
		"Accept to first response byte as the router saw it, over successful requests only, because a fast failure in a latency distribution makes an overloaded fleet look faster.")
	_, err := io.WriteString(w, b.String())
	return err
}

func (rt *Router) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", metricsContentType)
	if err := rt.metrics.write(w, rt.fleet.Members()); err != nil {
		rt.log.Debug("could not write /metrics", "err", err)
	}
}

// familyHeader writes the HELP and TYPE lines that open one metric family.
func familyHeader(b *strings.Builder, name, kind, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

// labelEscaper escapes the three characters the text format escapes in a label
// value, and nothing else. strconv.Quote would also turn non-ASCII into \u
// sequences, which the format does not have and a scraper would read literally.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// quoteLabel quotes one label value. Replica ids come off the command line, so
// they are escaped rather than trusted to be plain.
func quoteLabel(value string) string {
	return `"` + labelEscaper.Replace(value) + `"`
}

// overheadBuckets resolve the router's own cost, measured at a p50 of 135 µs
// and a p99 of 318 µs against a 1 ms gate. Prometheus's default buckets start
// at 5 ms, and would put every request in the first of them and report a p99
// of nearly 5 ms — a live figure fifteen times the recorded one.
var overheadBuckets = []float64{25e-6, 50e-6, 100e-6, 150e-6, 200e-6, 300e-6, 500e-6, 750e-6, 1e-3, 2.5e-3, 5e-3, 10e-3, 50e-3}

// ttftBuckets are densest between the latency floor's TTFT p50 of 329 ms and
// the SLO of 990 ms derived from it, which is where a policy's p50 and p99 live
// until the fleet saturates, and run out to a minute for when it has. The SLO
// is deliberately not a bound: it is derived per characterization and moves,
// and a bucket pinned to one derivation would be wrong after the next.
var ttftBuckets = []float64{0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10, 30, 60}

// histogram is a Prometheus histogram of durations: per-bucket counts rendered
// cumulatively, with a sum and a count that agree with them. It is not safe for
// concurrent use; metrics holds its mutex around every call.
type histogram struct {
	bounds []float64 // seconds, ascending
	counts []int64   // one per bound, plus a trailing +Inf bucket
	// sum is kept in whole nanoseconds, which is what the rows carry, so the
	// live total and the rows' total are the same integer rather than two
	// float sums that drift apart by rounding.
	sum time.Duration
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{bounds: bounds, counts: make([]int64, len(bounds)+1)}
}

// observe counts d in the first bucket whose bound it is at or below, which is
// what Prometheus's le means, or in +Inf past the last one.
func (h *histogram) observe(d time.Duration) {
	i, _ := slices.BinarySearch(h.bounds, d.Seconds())
	h.counts[i]++
	h.sum += d
}

// snapshot copies the histogram so it can be rendered outside the lock.
func (h *histogram) snapshot() histogram {
	return histogram{bounds: h.bounds, counts: slices.Clone(h.counts), sum: h.sum}
}

// render writes the whole family: its HELP and TYPE lines, then the bucket, sum
// and count series, so the family's name is written down once. The count is the
// +Inf bucket's cumulative total rather than a counter of its own, so the two
// cannot disagree.
func (h histogram) render(b *strings.Builder, name, help string) {
	familyHeader(b, name, "histogram", help)
	var cumulative int64
	for i, bound := range h.bounds {
		cumulative += h.counts[i]
		fmt.Fprintf(b, "%s_bucket{le=%s} %d\n", name, quoteLabel(strconv.FormatFloat(bound, 'g', -1, 64)), cumulative)
	}
	cumulative += h.counts[len(h.bounds)]
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, cumulative)
	fmt.Fprintf(b, "%s_sum %s\n", name, strconv.FormatFloat(h.sum.Seconds(), 'g', -1, 64))
	fmt.Fprintf(b, "%s_count %d\n", name, cumulative)
}
