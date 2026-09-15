package disagg_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/disagg"
	"github.com/yuchia329/kvroute/internal/gpu"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

func sixGPUs(t *testing.T) gpu.Topology {
	t.Helper()
	topo, err := gpu.ParseTopology(gputest.SixGPUTopology)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	return topo
}

// copies is one group of timed transfers of the same kind, one per duration.
func copies(path, condition string, src, dst, hostNode int, bytes int64, seconds ...float64) []disagg.Transfer {
	var out []disagg.Transfer
	for rep, s := range seconds {
		out = append(out, disagg.Transfer{
			Path: path, Condition: condition, Src: src, Dst: dst, HostNode: hostNode,
			Bytes: bytes, Rep: rep, Seconds: s, LinkSamples: 5, LinkGenMin: 3, LinkWidthMin: 16,
		})
	}
	return out
}

func analyse(t *testing.T, in disagg.Inputs) disagg.Analysis {
	t.Helper()
	a, err := disagg.Analyse(in)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	return a
}

const gigabyte = 1_000_000_000

// Which pairs belong together is the host's own matrix's to say, not the row's:
// 0-1 and 4-5 sit behind one bridge each, 0-4 crosses the sockets. A class is
// reported by its typical transfer and by its slowest pair, because a fleet
// built on it would have to live with the slowest pair it placed a transfer on.
func TestCardToCardCopiesAreGroupedByTheHostsTopologyClass(t *testing.T) {
	var transfers []disagg.Transfer
	transfers = append(transfers, copies("bounce", "alone", 0, 1, 0, gigabyte, 0.1, 0.1, 0.1)...)       // 10 GB/s
	transfers = append(transfers, copies("bounce", "alone", 1, 0, 0, gigabyte, 0.1, 0.1, 0.1)...)       // 10 GB/s
	transfers = append(transfers, copies("bounce", "alone", 4, 5, 1, gigabyte, 0.125, 0.125, 0.125)...) // 8 GB/s
	transfers = append(transfers, copies("bounce", "alone", 0, 4, 0, gigabyte, 0.2, 0.2, 0.2)...)       // 5 GB/s

	a := analyse(t, disagg.Inputs{Topology: sixGPUs(t), Transfers: transfers})

	pix, ok := a.Link("bounce", "PIX", "alone", gigabyte)
	if !ok {
		t.Fatal("no PIX summary")
	}
	if pix.Transfers != 9 {
		t.Errorf("PIX holds %d transfers, want 9: 0→1, 1→0 and 4→5, three each", pix.Transfers)
	}
	if pix.TypicalGBps != 10 {
		t.Errorf("PIX typical = %v GB/s, want 10: six of its nine transfers ran at 10", pix.TypicalGBps)
	}
	if pix.SlowestGBps != 8 || pix.SlowestSrc != 4 || pix.SlowestDst != 5 || pix.SlowestHostNode != 1 {
		t.Errorf("PIX slowest = %v GB/s on %d→%d via node %d, want 8 on 4→5 via node 1",
			pix.SlowestGBps, pix.SlowestSrc, pix.SlowestDst, pix.SlowestHostNode)
	}
	if pix.LinkGenMin != 3 {
		t.Errorf("PIX lowest link generation seen = %d, want 3", pix.LinkGenMin)
	}

	sys, ok := a.Link("bounce", "SYS", "alone", gigabyte)
	if !ok || sys.TypicalGBps != 5 || sys.Transfers != 3 {
		t.Errorf("SYS = %+v (found %v), want 3 transfers at 5 GB/s", sys, ok)
	}
	if _, ok := a.Link("bounce", "NODE", "alone", gigabyte); ok {
		t.Error("reported a NODE summary, and no NODE pair was measured")
	}
}

// A copy between one card and host memory has no second card to name a link
// by. What matters instead is whether the host buffer sat on the card's own
// NUMA node, since a bounce between two cards is built out of two of these legs
// and on a SYS pair one of them is always the far one.
func TestHostCopiesAreClassedByWhetherTheBufferWasOnTheCardsOwnNode(t *testing.T) {
	var transfers []disagg.Transfer
	transfers = append(transfers, copies("h2d", "alone", -1, 0, 0, gigabyte, 0.08, 0.08)...) // card 0 is on node 0
	transfers = append(transfers, copies("d2h", "alone", 4, -1, 1, gigabyte, 0.08, 0.08)...) // card 4 is on node 1
	transfers = append(transfers, copies("h2d", "alone", -1, 0, 1, gigabyte, 0.1, 0.1)...)
	transfers = append(transfers, copies("h2d", "alone", -1, 5, 0, gigabyte, 0.1, 0.1)...)

	a := analyse(t, disagg.Inputs{Topology: sixGPUs(t), Transfers: transfers})

	if local, ok := a.Link("h2d", "local", "alone", gigabyte); !ok || local.Transfers != 2 || local.TypicalGBps != 12.5 {
		t.Errorf("h2d local = %+v (found %v), want card 0 from node 0: 2 transfers at 12.5 GB/s", local, ok)
	}
	if remote, ok := a.Link("h2d", "remote", "alone", gigabyte); !ok || remote.Transfers != 4 || remote.TypicalGBps != 10 {
		t.Errorf("h2d remote = %+v (found %v), want card 0 from node 1 and card 5 from node 0: 4 at 10 GB/s", remote, ok)
	}
	if local, ok := a.Link("d2h", "local", "alone", gigabyte); !ok || local.Transfers != 2 {
		t.Errorf("d2h local = %+v (found %v), want card 4 to node 1: 2 transfers", local, ok)
	}
}

// movedAlone is one card-to-card copy of each length's KV, at the pinned
// model's 128 KiB a token: the least a prefill figure has to stand beside.
func movedAlone(lengths ...int) []disagg.Transfer {
	var out []disagg.Transfer
	for _, n := range lengths {
		out = append(out, copies("bounce", "alone", 0, 1, 0, int64(n)*131072, 0.01)...)
	}
	return out
}

func prefill(tokens int, engine, client float64) disagg.PrefillRow {
	return disagg.PrefillRow{
		Tokens: tokens, EnginePrefillSeconds: engine, ClientSeconds: client, EngineTTFTSeconds: engine,
		PromptTokens: tokens, CachedTokens: 0, RequestsCounted: 1,
	}
}

// A prefill figure is only a prefill figure if the engine computed every token
// of the prompt, and only one request's if the engine counted exactly one. A
// row the cache helped, or whose cached count the engine did not report, is
// excluded rather than averaged in, and the exclusions are counted so a length
// resting on fewer requests than it was sent says so.
func TestPrefillIsTheEnginesMedianOverRequestsItComputedInFull(t *testing.T) {
	cached := prefill(2048, 0.05, 0.06)
	cached.CachedTokens = 16
	unreported := prefill(2048, 0.05, 0.06)
	unreported.CachedTokens = -1
	uncounted := prefill(2048, 0, 0.5)
	uncounted.RequestsCounted = 0
	resized := prefill(2048, 0.5, 0.52)
	resized.PromptTokens = 2049

	geometry, err := disagg.ParseModelConfig([]byte(llama31_8B))
	if err != nil {
		t.Fatal(err)
	}
	a := analyse(t, disagg.Inputs{Geometry: geometry, Topology: sixGPUs(t), Transfers: movedAlone(256, 2048),
		Prefills: []disagg.PrefillRow{
			prefill(2048, 0.4, 0.42), prefill(2048, 0.5, 0.52), prefill(2048, 0.6, 0.62),
			cached, unreported, uncounted, resized,
			prefill(256, 0.06, 0.07),
		}})

	p, ok := a.Prefill(2048)
	if !ok {
		t.Fatal("no summary for 2,048 tokens")
	}
	if p.Requests != 3 || p.Excluded != 4 {
		t.Errorf("2,048 tokens rests on %d requests with %d excluded, want 3 and 4", p.Requests, p.Excluded)
	}
	if p.EnginePrefillSeconds != 0.5 || p.ClientSeconds != 0.52 {
		t.Errorf("2,048 tokens = %v s engine, %v s client; want the medians 0.5 and 0.52", p.EnginePrefillSeconds, p.ClientSeconds)
	}
	if short, ok := a.Prefill(256); !ok || short.Requests != 1 || short.EnginePrefillSeconds != 0.06 {
		t.Errorf("256 tokens = %+v (found %v), want 1 request at 0.06 s", short, ok)
	}
}

// The arithmetic §8 asks for: a request's KV, derived from the model, divided
// by the bandwidth measured on each class of link, set against the prefill it
// follows. When the copy measured was exactly one request's KV — 2,048 tokens at
// 128 KiB is 268,435,456 bytes — moving that KV takes exactly as long as the
// measured copy did: 25 ms typically and 50 ms on the slowest pair, a tenth of
// a 0.5 s prefill.
func TestTransferCostIsTheRequestsKVOverTheMeasuredBandwidthAgainstItsPrefill(t *testing.T) {
	geometry, err := disagg.ParseModelConfig([]byte(llama31_8B))
	if err != nil {
		t.Fatal(err)
	}
	const kv = 2048 * 131072
	var transfers []disagg.Transfer
	transfers = append(transfers, copies("bounce", "alone", 0, 1, 0, kv, 0.025, 0.025, 0.025)...)
	transfers = append(transfers, copies("bounce", "alone", 1, 0, 0, kv, 0.025, 0.025, 0.025)...)
	transfers = append(transfers, copies("bounce", "alone", 4, 5, 1, kv, 0.05, 0.05, 0.05)...)

	a := analyse(t, disagg.Inputs{Geometry: geometry, Topology: sixGPUs(t), Transfers: transfers,
		Prefills: []disagg.PrefillRow{prefill(2048, 0.5, 0.52)}})

	c, ok := a.Cost(2048, "bounce", "PIX", "alone")
	if !ok {
		t.Fatal("no cost for 2,048 tokens over PIX")
	}
	if c.KVBytes != 268435456 {
		t.Errorf("KV = %d bytes, want 268,435,456", c.KVBytes)
	}
	if c.PrefillSeconds != 0.5 {
		t.Errorf("prefill = %v s, want the measured 0.5", c.PrefillSeconds)
	}
	near := func(got, want float64) bool { return got > want*0.999999 && got < want*1.000001 }
	if !near(c.TypicalSeconds, 0.025) || !near(c.WorstSeconds, 0.05) {
		t.Errorf("transfer = %v s typical, %v s worst; want 0.025 and 0.05", c.TypicalSeconds, c.WorstSeconds)
	}
	if !near(c.WorstShare(), 0.1) {
		t.Errorf("worst transfer is %v of prefill, want 0.1", c.WorstShare())
	}
}

// measure.sh states the prompt lengths and the copy sizes as two lists, the
// second worked out by hand from the first. If they drift apart, some length's
// KV was never moved, and the arithmetic would quietly leave that length out of
// the answer. It refuses instead.
func TestALengthWhoseKVWasNeverMovedAloneIsRefused(t *testing.T) {
	geometry, err := disagg.ParseModelConfig([]byte(llama31_8B))
	if err != nil {
		t.Fatal(err)
	}
	// 2,048 tokens is 268,435,456 bytes of KV; only a gigabyte was moved.
	transfers := copies("bounce", "alone", 0, 1, 0, gigabyte, 0.1, 0.1)

	_, err = disagg.Analyse(disagg.Inputs{Geometry: geometry, Topology: sixGPUs(t), Transfers: transfers,
		Prefills: []disagg.PrefillRow{prefill(2048, 0.5, 0.52)}})
	if err == nil {
		t.Fatal("analysed a 2,048-token prefill against copies that never moved its 268,435,456 bytes")
	}
}

// A length every one of whose requests was excluded has no prefill to set a
// transfer against, and would drop out of the answer without a word — the
// table simply one row shorter. That is refused, like a length whose KV was
// never moved.
func TestALengthWithNoWholePrefillIsRefused(t *testing.T) {
	geometry, err := disagg.ParseModelConfig([]byte(llama31_8B))
	if err != nil {
		t.Fatal(err)
	}
	unreported := prefill(2048, 0.5, 0.52)
	unreported.CachedTokens = -1 // the engine did not say, so a whole prefill is unproven

	_, err = disagg.Analyse(disagg.Inputs{Geometry: geometry, Topology: sixGPUs(t), Transfers: movedAlone(2048),
		Prefills: []disagg.PrefillRow{unreported}})
	if err == nil {
		t.Fatal("analysed a 2,048-token length none of whose requests was a whole prefill")
	}
}

// The recorded matrix classes every pair, and each row carries the link NVML
// named when the copy ran. If the two disagree, the rows and the matrix came off
// different hosts, or the cards were numbered differently, and every class in
// the report would hold the wrong pairs.
func TestARowWhoseLinkDisagreesWithTheTopologyIsRefused(t *testing.T) {
	transfers := copies("peer", "alone", 0, 1, 0, gigabyte, 0.1)
	transfers[0].Link = "SYS" // the matrix says 0-1 is PIX

	if _, err := disagg.Analyse(disagg.Inputs{Topology: sixGPUs(t), Transfers: transfers}); err == nil {
		t.Fatal("analysed a 0→1 copy recorded as SYS against a matrix that says PIX")
	}
}
