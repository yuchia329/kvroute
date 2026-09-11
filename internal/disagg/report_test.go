package disagg_test

import (
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/disagg"
)

// The report is where the result is read, so every figure the conclusion rests
// on has to be in it, beside what it was measured from: the KV a token costs
// and why, whether any pair could reach the other directly, each class's
// bandwidth and its slowest pair, the link generation the copies actually ran
// at, the evidence the busy cards were busy, and the transfer against the
// prefill.
func TestReportStatesTheArithmeticAndEverythingItRestsOn(t *testing.T) {
	geometry, err := disagg.ParseModelConfig([]byte(llama31_8B))
	if err != nil {
		t.Fatal(err)
	}
	const kv = 2048 * 131072
	var transfers []disagg.Transfer
	transfers = append(transfers, copies("bounce", "alone", 0, 1, 0, kv, 0.025, 0.025, 0.025)...) // 10.74 GB/s
	transfers = append(transfers, copies("bounce", "alone", 1, 0, 0, kv, 0.025, 0.025, 0.025)...) // 10.74 GB/s
	transfers = append(transfers, copies("bounce", "alone", 4, 5, 1, kv, 0.05, 0.05, 0.05)...)    // 5.37 GB/s, the slowest pair
	transfers = append(transfers, copies("h2d", "alone", -1, 0, 0, kv, 0.034)...)

	a := analyse(t, disagg.Inputs{
		Geometry: geometry, Topology: sixGPUs(t), Transfers: transfers,
		Prefills:     []disagg.PrefillRow{prefill(2048, 0.5, 0.52)},
		BusyLoopGBps: []float64{418.7, 420.1},
		Runs: []disagg.BandwidthRun{{HostNode: 0, HostPagesByNode: map[string]int{"0": 1},
			PeerAccess: map[string]bool{"0-1": false, "1-0": false}}},
	})
	report := a.Report()

	for _, want := range []string{
		"131,072 bytes", // a token's KV, and the dimensions it is made of
		"32 layers",
		"8 KV heads",
		"no pair",   // peer access
		"10.74",     // PIX bounce, typical
		"5.37",      // and its slowest pair
		"4→5",       // named
		"gen 3 x16", // the link the copies ran on
		"418.7",     // the busy loop's slowest card
		"256 MiB",   // 2,048 tokens of KV
		"500.0 ms",  // its prefill
		"50.0 ms",   // moved on the slowest PIX pair
		"10.0%",     // a tenth of the prefill
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report does not state %q:\n%s", want, report)
		}
	}
}
