package router_test

import (
	"io"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/stats"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// unidentifiedRequest carries a system prompt and nothing else. A system prompt
// alone identifies no conversation (see internal/session), so session affinity
// has nothing to hash and says so in its reason.
const unidentifiedRequest = `{"model":"m","messages":[{"role":"system","content":"be brief"}],"stream":false}`

// scrape reads the router's /metrics the way Prometheus does, and fails on a
// Content-Type Prometheus 3 would refuse (see metricsContentType).
func scrape(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("/metrics Content-Type = %q, want the Prometheus text format, text/plain; version=0.0.4", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	return string(body)
}

// sample returns the value of the series named name whose labels are exactly
// labels, and whether the exposition had it at all.
//
// Labels are matched as a set rather than as text, because Prometheus reads them
// as one: a test that matched the rendered string would fail on a reordering
// that changes nothing any dashboard sees.
func sample(t *testing.T, exposition, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	for line := range strings.SplitSeq(exposition, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, raw, found := strings.Cut(line, " ")
		if !found {
			t.Fatalf("exposition line %q has no value", line)
		}
		base, set, _ := strings.Cut(series, "{")
		if base != name || !maps.Equal(labelSet(t, strings.TrimSuffix(set, "}")), labels) {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("series %s has value %q: %v", series, raw, err)
		}
		return value, true
	}
	return 0, false
}

// labelSet splits `a="1",b="2"` into its pairs. The router's label values are
// replica ids, policy names and decision reasons, none of which carries a comma.
func labelSet(t *testing.T, set string) map[string]string {
	t.Helper()
	labels := map[string]string{}
	for pair := range strings.SplitSeq(set, ",") {
		if pair == "" {
			continue
		}
		key, quoted, _ := strings.Cut(pair, "=")
		value, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatalf("label %s has unquotable value %s: %v", key, quoted, err)
		}
		labels[key] = value
	}
	return labels
}

// TestDecisionMixIsCountedByReason: the decision mix is a reported result, so the
// live view of it keeps reasons apart exactly as the rows do.
//
// Session affinity is the policy that shows why. It makes two different
// decisions — a conversation it has an identity to hash, and a request it has
// none for, which it rotates — and a counter that lumped them together would
// show one busy session where there is only a pile of unidentified requests.
func TestDecisionMixIsCountedByReason(t *testing.T) {
	_, a := startFake(t, fakereplica.Config{ID: "replica-0"})
	_, b := startFake(t, fakereplica.Config{ID: "replica-1"})
	r := startRouterWith(t, policy.NewSessionAffinity(), "replica-0="+a, "replica-1="+b)

	for range 3 {
		post(t, r.url, blockingRequest)
	}
	for range 2 {
		post(t, r.url, unidentifiedRequest)
	}
	r.rows.wait(t, 5)

	exposition := scrape(t, r.url)
	for reason, want := range map[policy.Reason]float64{
		policy.ReasonSessionAffinity:     3,
		policy.ReasonSessionUnidentified: 2,
	} {
		labels := map[string]string{"policy": policy.SessionAffinityName, "reason": string(reason)}
		got, found := sample(t, exposition, "kvroute_decisions_total", labels)
		if !found {
			t.Errorf("no kvroute_decisions_total series for %s", reason)
			continue
		}
		if got != want {
			t.Errorf("kvroute_decisions_total{reason=%q} = %v, want %v", reason, got, want)
		}
	}
}

// TestInflightIsLiveForEveryReplica: inflight is the signal the load-aware
// policies decide on, and one only the rows could reconstruct afterwards cannot
// be watched during a run. The router counts it exactly, so what a scrape sees
// is the count at that moment.
//
// The idle replica is checked as well as the busy one. A replica with nothing in
// flight has to read as zero rather than be missing, or a panel of the fleet
// shows only the replicas that happen to be busy.
func TestInflightIsLiveForEveryReplica(t *testing.T) {
	arrived := make(chan string, 2)
	release := make(chan struct{})
	a := startGate(t, "replica-0", arrived, release, true)
	b := startGate(t, "replica-1", arrived, release, true)
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+a, "replica-1="+b)

	done := make(chan error, 1)
	go func() {
		_, err := send(r.url, blockingRequest)
		done <- err
	}()
	var held string
	select {
	case held = <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("the request never reached a replica")
	}
	idle := map[string]string{"replica-0": "replica-1", "replica-1": "replica-0"}[held]

	exposition := scrape(t, r.url)
	for id, want := range map[string]float64{held: 1, idle: 0} {
		got, found := sample(t, exposition, "kvroute_replica_inflight", map[string]string{"replica": id})
		if !found {
			t.Errorf("no kvroute_replica_inflight series for %s", id)
			continue
		}
		if got != want {
			t.Errorf("kvroute_replica_inflight{replica=%q} = %v while the request was held, want %v", id, got, want)
		}
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("send: %v", err)
	}
	r.drains(t)
	if got, _ := sample(t, scrape(t, r.url), "kvroute_replica_inflight", map[string]string{"replica": held}); got != 0 {
		t.Errorf("kvroute_replica_inflight{replica=%q} = %v after the request finished, want 0", held, got)
	}
}

// TestADrainingReplicaStaysOnTheInflightPanel: a drain is finished when the
// replica's inflight reaches zero, so that count is what an operator watches
// while one runs. A replica out of rotation is absent from the snapshot a
// policy routes from, by design, and a panel read off that snapshot would lose
// the replica exactly while it drains.
//
// It waits on the row rather than on the fleet: a request's release runs before
// its row is written, so a written row means the count has come down.
func TestADrainingReplicaStaysOnTheInflightPanel(t *testing.T) {
	arrived := make(chan string, 2)
	release := make(chan struct{})
	a := startGate(t, "replica-0", arrived, release, true)
	b := startGate(t, "replica-1", arrived, release, true)
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+a, "replica-1="+b)

	done := make(chan error, 1)
	go func() {
		_, err := send(r.url, blockingRequest)
		done <- err
	}()
	var held string
	select {
	case held = <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("the request never reached a replica")
	}
	// Drained through the router's own admin surface, the way an operator does
	// it, rather than by reaching into the fleet.
	if status, _ := admin(t, r.url, held, "drain"); status != http.StatusOK {
		t.Fatalf("drain %s: status %d, want 200", held, status)
	}

	got, found := sample(t, scrape(t, r.url), "kvroute_replica_inflight", map[string]string{"replica": held})
	if !found || got != 1 {
		t.Errorf("kvroute_replica_inflight{replica=%q} = %v (present: %v) while it drained, want 1: a drain is watched on this count", held, got, found)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("send: %v", err)
	}
	r.rows.wait(t, 1)
	got, found = sample(t, scrape(t, r.url), "kvroute_replica_inflight", map[string]string{"replica": held})
	if !found || got != 0 {
		t.Errorf("kvroute_replica_inflight{replica=%q} = %v (present: %v) once drained, want 0: a finished drain reads as zero, not as a replica that is gone", held, got, found)
	}
}

// histogram reads one histogram family off the router's exposition with the
// parser that reads the engine's, and fails on one Prometheus could not take a
// quantile of: buckets that ever fall, or a +Inf bucket that disagrees with the
// count.
func histogram(t *testing.T, exposition, name string) vllmmetrics.Distribution {
	t.Helper()
	h := vllmmetrics.ReadHistogramFrom(exposition, name)
	if !h.Read {
		t.Fatalf("no %s histogram in the exposition", name)
	}
	for i := 1; i < len(h.Cumulative); i++ {
		if h.Cumulative[i] < h.Cumulative[i-1] {
			t.Fatalf("%s buckets fall from %v to %v at le=%v: they must be cumulative", name, h.Cumulative[i-1], h.Cumulative[i], h.Bounds[i])
		}
	}
	inf, found := sample(t, exposition, name+"_bucket", map[string]string{"le": "+Inf"})
	if !found || inf != h.Count {
		t.Fatalf("%s +Inf bucket = %v (present: %v), count = %v: the two must agree", name, inf, found, h.Count)
	}
	return h
}

// TestRouterOverheadDistributionAgreesWithTheRows: router overhead is reported
// apart from TTFT so the router's own cost is never hidden inside the fleet's
// latency, and the live view of it has to describe the requests the rows
// describe — one observation per dispatch, the same time for each.
//
// The p99 check is on the buckets. The router's overhead is a few hundred
// microseconds against a 1 ms gate, and a histogram whose first bound sat at
// Prometheus's default 5 ms would put every request in one bucket and report a
// p99 of nearly 5 ms: fifteen times the 318 µs the rows record on the fleet.
// Within a factor of two of the rows' own p99 is what buckets that resolve the
// gate give.
func TestRouterOverheadDistributionAgreesWithTheRows(t *testing.T) {
	_, replicaURL := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+replicaURL)

	const requests = 5
	for range requests {
		post(t, r.url, streamingRequest)
	}
	rows := r.rows.wait(t, requests)

	got := histogram(t, scrape(t, r.url), "kvroute_router_overhead_seconds")
	if got.Count != requests {
		t.Errorf("count = %v, want %d: one per request the router dispatched", got.Count, requests)
	}
	var sum float64
	overheads := make([]time.Duration, 0, len(rows))
	for _, row := range rows {
		sum += time.Duration(row.RouterOverheadNs).Seconds()
		overheads = append(overheads, time.Duration(row.RouterOverheadNs))
	}
	if math.Abs(got.Sum-sum) > 1e-9 {
		t.Errorf("sum = %vs, but the rows record %vs of router overhead", got.Sum, sum)
	}

	slices.Sort(overheads)
	recorded := stats.Quantile(overheads, 0.99).Seconds()
	live, resolved := got.Quantile(0.99)
	if !resolved {
		t.Fatalf("p99 ran off the last finite bucket: the rows put it at %v", time.Duration(recorded*float64(time.Second)))
	}
	if live > 2*recorded || live < recorded/2 {
		t.Errorf("live p99 = %v, the rows' p99 = %v: the buckets cannot resolve the router's overhead",
			time.Duration(live*float64(time.Second)), time.Duration(recorded*float64(time.Second)))
	}
}

// TestTTFTDistributionCountsOnlySuccesses: under overload a replica rejects in
// milliseconds, and a latency distribution with the rejections folded in makes
// an overloaded fleet look faster. The rows keep failures out of every
// percentile, so the live distribution does too.
//
// A failed request here does receive a first byte — the replica's error body —
// so this is not the same as counting whatever had a TTFT. The router's own
// overhead still counts all four: it dispatched every one of them and paid for
// it whatever the replica then said.
func TestTTFTDistributionCountsOnlySuccesses(t *testing.T) {
	_, serving := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 2})
	failing, failingURL := startFake(t, fakereplica.Config{ID: "replica-1"})
	failing.SetFailure(&fakereplica.Failure{
		Status:  http.StatusServiceUnavailable,
		Type:    "ServiceUnavailableError",
		Message: "engine is out of KV blocks",
	})
	r := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+serving, "replica-1="+failingURL)

	// Round-robin alternates, so four requests are two successes and two
	// failures.
	for range 4 {
		post(t, r.url, streamingRequest)
	}
	successes := 0
	for _, row := range r.rows.wait(t, 4) {
		if row.Outcome == record.OutcomeSuccess {
			successes++
		}
	}
	if successes != 2 {
		t.Fatalf("%d of 4 requests succeeded, want 2: the test needs both outcomes", successes)
	}

	exposition := scrape(t, r.url)
	if got := histogram(t, exposition, "kvroute_ttft_seconds").Count; got != 2 {
		t.Errorf("ttft count = %v, want 2: the two failed requests must stay out of the latency distribution", got)
	}
	if got := histogram(t, exposition, "kvroute_router_overhead_seconds").Count; got != 4 {
		t.Errorf("overhead count = %v, want 4: the router dispatched all four", got)
	}
}
