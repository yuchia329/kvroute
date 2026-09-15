package bench_test

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// TestRouterRowsLoadFromEveryFormARunKeepsThemIn. A router appends JSONL, a
// sweep compacts it to Parquet, and a reference run commits it gzipped — and
// the overhead figure has to come off whichever of them a checkout holds.
func TestRouterRowsLoadFromEveryFormARunKeepsThemIn(t *testing.T) {
	a := routed(policy.RoundRobinName, 100*time.Microsecond)
	a.RequestID = "a"
	b := routed(policy.PrefixAffinityName, 200*time.Microsecond)
	b.RequestID = "b"
	rows := []record.Request{a, b}
	dir := t.TempDir()

	jsonl := filepath.Join(dir, "router.jsonl")
	w, err := record.Open[record.Request](jsonl)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	plain, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := os.Create(filepath.Join(dir, "router.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(gz)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if err := parquet.WriteFile(filepath.Join(dir, "router.parquet"), rows); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"router.jsonl", "router.jsonl.gz", "router.parquet"} {
		got, err := bench.LoadRouterRows(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) != 2 || got[1].RequestID != "b" || got[1].RouterOverheadNs != 200_000 || got[1].Policy != policy.PrefixAffinityName {
			t.Errorf("%s read back as %+v, want the two rows written", name, got)
		}
	}

	if _, err := bench.LoadRouterRows(filepath.Join(dir, "router.csv")); err == nil {
		t.Error("a file in no form a run keeps router rows in was read without complaint")
	}
}

// routed is one router row that reached a replica after the given overhead.
func routed(policyName string, overhead time.Duration) record.Request {
	return record.Request{Policy: policyName, Replica: "replica-0", RouterOverheadNs: overhead.Nanoseconds(), Outcome: record.OutcomeSuccess}
}

// TestRouterOverheadIsReportedPerPolicyOverDispatchedRequests. Router overhead is
// the router's own cost, accept to dispatch, and it is reported so a reader can
// rule out the router having added latency the baselines did not pay. A request
// the router never dispatched paid no dispatch cost to measure, so it is not a
// zero in the distribution.
func TestRouterOverheadIsReportedPerPolicyOverDispatchedRequests(t *testing.T) {
	exact := func(overhead, tokenize time.Duration) record.Request {
		r := routed(policy.ExactResidencyName, overhead)
		r.TokenizeNs = tokenize.Nanoseconds()
		return r
	}
	rows := []record.Request{
		exact(time.Millisecond, 800*time.Microsecond),
		routed(policy.RoundRobinName, 300*time.Microsecond),
		routed(policy.RoundRobinName, 100*time.Microsecond),
		{Policy: policy.RoundRobinName, Outcome: record.OutcomeDropped}, // never placed
		exact(2*time.Millisecond, 1500*time.Microsecond),
		routed(policy.RoundRobinName, 200*time.Microsecond),
	}

	got := bench.RouterOverhead(rows)

	if len(got) != 2 || got[0].Policy != policy.RoundRobinName || got[1].Policy != policy.ExactResidencyName {
		t.Fatalf("got %+v, want round robin then exact residency, in the comparison's order", got)
	}
	rr := got[0]
	if rr.Dispatched != 3 || rr.P50Ns != 200_000 || rr.P99Ns != 300_000 || rr.MaxNs != 300_000 {
		t.Errorf("round robin = %+v, want 3 dispatched, p50 200µs, p99 and max 300µs", rr)
	}
	if rr.Tokenized != 0 {
		t.Errorf("round robin tokenized %d prompts; it never asks the engine", rr.Tokenized)
	}
	ex := got[1]
	if ex.Dispatched != 2 || ex.P50Ns != 1_000_000 || ex.P99Ns != 2_000_000 {
		t.Errorf("exact residency = %+v, want 2 dispatched, p50 1ms, p99 2ms", ex)
	}
	if ex.Tokenized != 2 || ex.TokenizeP50Ns != 800_000 || ex.TokenizeP99Ns != 1_500_000 {
		t.Errorf("exact residency tokenize = %+v, want 2 prompts, p50 800µs, p99 1.5ms", ex)
	}

	overhead := bench.Overhead{Sources: []string{"runs/pressure/router.jsonl"}, Policies: got}
	figure := overhead.Figure()
	if figure.Policies[0].P50Us != 200 || figure.Policies[0].TokenizeP50Us != nil {
		t.Errorf("round robin's figure = %+v, want 200 µs and no tokenize percentile at all", figure.Policies[0])
	}
	if figure.Policies[1].TokenizeP50Us == nil || *figure.Policies[1].TokenizeP50Us != 800 {
		t.Errorf("exact residency's figure = %+v, want a tokenize p50 of 800 µs", figure.Policies[1])
	}

	report := overhead.Report()
	for _, want := range []string{
		"| round_robin | 3 | 200 µs | 300 µs | 300 µs | — | — |",
		"| exact_residency | 2 | 1000 µs | 2000 µs | 2000 µs | 800 µs | 1500 µs |",
		"runs/pressure/router.jsonl",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
}
