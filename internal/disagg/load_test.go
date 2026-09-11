package disagg_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuchia329/kvroute/internal/disagg"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

// writeRun lays out a measurement directory as ops/pcie/measure.sh leaves it.
func writeRun(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const cleanMeta = `{"host_node": %d, "host_pages_by_node": {"%d": 24578}, "foreign": {},
  "peer_access": {"0-1": false, "1-0": false}}`

func meta(node int) string {
	return fmt.Sprintf(cleanMeta, node, node)
}

const onePrefillRow = `{"tokens": 256, "rep": 0, "client_seconds": 0.07, "engine_prefill_seconds": 0.06, "engine_ttft_seconds": 0.061, "requests_counted": 1, "prompt_tokens": 256, "cached_tokens": 0}
`

const prefillMeta = `{"replica": "http://127.0.0.1:8001", "lengths": [256], "repetitions": 1}`

// A measurement directory is read whole: the model's config, the host's
// matrix, both NUMA nodes' copies and the prefill. The busy loop's rows are the
// evidence the cards were busy, not copies between them, and are kept apart so
// no class ever holds them.
func TestLoadReadsAMeasurementDirectoryAsMeasureShLeavesIt(t *testing.T) {
	dir := writeRun(t, map[string]string{
		"model-config.json": llama31_8B,
		"topology.txt":      gputest.SixGPUTopology,
		"bandwidth-node0/rows.jsonl": `{"path": "bounce", "condition": "alone", "src": 0, "dst": 1, "link": "PIX", "host_node": 0, "bytes": 1000, "rep": 0, "seconds": 0.001, "link_samples": 3, "link_gen_min": 3, "link_width_min": 16}
{"path": "churn", "condition": "busy", "src": 0, "dst": 1, "bytes": 1000, "host_node": 0, "churn_gbps": {"0": 418.7, "1": 420.1}}
`,
		"bandwidth-node0/meta.json": meta(0),
		"bandwidth-node1/rows.jsonl": `{"path": "peer", "condition": "alone", "src": 4, "dst": 5, "link": "PIX", "host_node": 1, "bytes": 1000, "rep": 0, "seconds": 0.002, "link_samples": 3, "link_gen_min": 3, "link_width_min": 16}
`,
		"bandwidth-node1/meta.json": meta(1),
		"prefill.jsonl":             onePrefillRow,
		"prefill-meta.json":         prefillMeta,
	})

	in, err := disagg.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := in.Geometry.BytesPerToken(); got != 131072 {
		t.Errorf("bytes per token = %d, want 131072 from the config", got)
	}
	if len(in.Topology.GPUs) != 6 {
		t.Errorf("topology names %d cards, want 6", len(in.Topology.GPUs))
	}
	if len(in.Transfers) != 2 {
		t.Fatalf("read %d transfers, want 2: one per node, and the churn row is not one", len(in.Transfers))
	}
	if in.Transfers[1].Src != 4 || in.Transfers[1].HostNode != 1 {
		t.Errorf("second transfer = %+v, want node 1's 4→5", in.Transfers[1])
	}
	if len(in.BusyLoopGBps) != 2 || min(in.BusyLoopGBps[0], in.BusyLoopGBps[1]) != 418.7 {
		t.Errorf("busy-loop rates = %v, want both cards' 418.7 and 420.1", in.BusyLoopGBps)
	}
	if len(in.Prefills) != 1 || in.Prefills[0].EnginePrefillSeconds != 0.06 {
		t.Errorf("prefill rows = %+v, want the one at 0.06 s", in.Prefills)
	}
	if len(in.Runs) != 2 || in.Runs[1].HostNode != 1 || in.Runs[0].PeerAccess["0-1"] {
		t.Errorf("runs = %+v, want node 0 then node 1, with peer access off", in.Runs)
	}
}

// bandwidth.py exits non-zero on either failure, but its rows stay on disk. A
// pass that shared the cards with someone else's job, or whose host buffer
// landed off the node it names, claims conditions it did not have, and is
// refused rather than analysed.
func TestLoadRefusesABandwidthPassThatWasNotClean(t *testing.T) {
	for name, m := range map[string]string{
		"a foreign process":       `{"host_node": 0, "host_pages_by_node": {"0": 24578}, "foreign": {"12345": {"card": 2, "used_mib": 900}}, "peer_access": {}}`,
		"pages on the other node": `{"host_node": 0, "host_pages_by_node": {"1": 24578}, "foreign": {}, "peer_access": {}}`,
		"pages on both nodes":     `{"host_node": 0, "host_pages_by_node": {"0": 12000, "1": 12578}, "foreign": {}, "peer_access": {}}`,
	} {
		dir := writeRun(t, map[string]string{
			"model-config.json":          llama31_8B,
			"topology.txt":               gputest.SixGPUTopology,
			"bandwidth-node0/rows.jsonl": `{"path": "bounce", "condition": "alone", "src": 0, "dst": 1, "link": "PIX", "host_node": 0, "bytes": 1000, "rep": 0, "seconds": 0.001}` + "\n",
			"bandwidth-node0/meta.json":  m,
			"prefill.jsonl":              onePrefillRow,
			"prefill-meta.json":          prefillMeta,
		})
		if _, err := disagg.Load(dir); err == nil {
			t.Errorf("%s: loaded a pass that was not clean", name)
		}
	}
}

// prefill.py writes its rows as it goes and its meta.json only at the end, so a
// prefill run that died partway leaves rows under their final name and no meta.
// Read as if whole, it would publish a prefill median over however many requests
// happened to land before the run stopped.
func TestLoadRefusesAPrefillRunThatDidNotFinish(t *testing.T) {
	dir := writeRun(t, map[string]string{
		"model-config.json":          llama31_8B,
		"topology.txt":               gputest.SixGPUTopology,
		"bandwidth-node0/rows.jsonl": `{"path": "bounce", "condition": "alone", "src": 0, "dst": 1, "link": "PIX", "host_node": 0, "bytes": 1000, "rep": 0, "seconds": 0.001}` + "\n",
		"bandwidth-node0/meta.json":  meta(0),
		"prefill.jsonl":              onePrefillRow,
	})
	if _, err := disagg.Load(dir); err == nil {
		t.Fatal("loaded a prefill run that wrote no prefill-meta.json, so never reached its end")
	}
}
