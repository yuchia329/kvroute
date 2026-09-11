package disagg

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// BandwidthRun is one pass of ops/pcie/bandwidth.py, as its meta.json records
// it: the NUMA node its host buffer was bound to, the versions it ran on, and
// the evidence that the conditions it claims held.
type BandwidthRun struct {
	HostNode int `json:"host_node"`
	// HostPagesByNode is where the host buffer's pages actually landed, read
	// back from /proc/self/numa_maps, keyed by node.
	HostPagesByNode map[string]int `json:"host_pages_by_node"`
	// PeerAccess is torch's answer, per ordered pair "a-b", to whether the
	// first card can reach the second's memory directly.
	PeerAccess map[string]bool `json:"peer_access"`
	// Foreign is every foreign process seen on any card during the pass.
	Foreign  map[string]json.RawMessage `json:"foreign"`
	Host     string                     `json:"host"`
	Driver   string                     `json:"driver"`
	Torch    string                     `json:"torch"`
	CUDA     string                     `json:"cuda"`
	Started  string                     `json:"started"`
	Finished string                     `json:"finished"`
}

// bandwidthRow is a line of rows.jsonl: a timed transfer, or the busy loop's
// achieved rate on the cards of one busy group.
type bandwidthRow struct {
	Transfer
	BusyLoopGBps map[string]float64 `json:"churn_gbps"`
}

// Load reads a measurement directory as ops/pcie/measure.sh leaves it: the
// model's config.json, the host's topology matrix, one bandwidth-node<N>
// directory per NUMA node the host buffer was bound to, and the prefill rows.
func Load(dir string) (Inputs, error) {
	var in Inputs

	config, err := os.ReadFile(filepath.Join(dir, "model-config.json"))
	if err != nil {
		return Inputs{}, fmt.Errorf("disagg: %w", err)
	}
	if in.Geometry, err = ParseModelConfig(config); err != nil {
		return Inputs{}, err
	}

	matrix, err := os.ReadFile(filepath.Join(dir, "topology.txt"))
	if err != nil {
		return Inputs{}, fmt.Errorf("disagg: %w", err)
	}
	if in.Topology, err = gpu.ParseTopology(string(matrix)); err != nil {
		return Inputs{}, err
	}

	runs, err := filepath.Glob(filepath.Join(dir, "bandwidth-node*"))
	if err != nil {
		return Inputs{}, fmt.Errorf("disagg: %w", err)
	}
	if len(runs) == 0 {
		return Inputs{}, fmt.Errorf("disagg: %s holds no bandwidth-node* directory; nothing was measured", dir)
	}
	for _, runDir := range runs {
		// meta.json is written once the pass has finished, so a pass that died
		// partway has rows and no meta, and is refused below by its absence.
		run, err := readJSON[BandwidthRun](filepath.Join(runDir, "meta.json"))
		if err != nil {
			return Inputs{}, err
		}
		// bandwidth.py fails on either of these, but leaves its rows behind.
		if len(run.Foreign) > 0 {
			return Inputs{}, fmt.Errorf("disagg: %s: %d foreign processes held the cards during this pass, so its copies timed their work too",
				runDir, len(run.Foreign))
		}
		if pages := run.HostPagesByNode; len(pages) != 1 || pages[fmt.Sprint(run.HostNode)] == 0 {
			return Inputs{}, fmt.Errorf("disagg: %s: the host buffer's pages landed on %v, not only on node %d, so every row states the wrong placement",
				runDir, pages, run.HostNode)
		}
		in.Runs = append(in.Runs, run)

		rows, err := readJSONL[bandwidthRow](filepath.Join(runDir, "rows.jsonl"))
		if err != nil {
			return Inputs{}, err
		}
		for _, r := range rows {
			if r.Path == "churn" {
				// In card order, so two loads of one directory agree.
				for _, card := range slices.Sorted(maps.Keys(r.BusyLoopGBps)) {
					in.BusyLoopGBps = append(in.BusyLoopGBps, r.BusyLoopGBps[card])
				}
				continue
			}
			in.Transfers = append(in.Transfers, r.Transfer)
		}
	}

	// prefill.py writes its rows as it goes and its meta only at the end, so
	// rows under their final name prove nothing about whether the run reached
	// it. Read as whole, a run that died partway would publish a prefill median
	// over however many requests happened to land first.
	if _, err := os.Stat(filepath.Join(dir, "prefill-meta.json")); err != nil {
		return Inputs{}, fmt.Errorf("disagg: %s holds no prefill-meta.json, so the prefill run never reached its end: %w", dir, err)
	}
	if in.Prefills, err = readJSONL[PrefillRow](filepath.Join(dir, "prefill.jsonl")); err != nil {
		return Inputs{}, err
	}
	return in, nil
}

func readJSON[T any](path string) (T, error) {
	var v T
	data, err := os.ReadFile(path)
	if err != nil {
		return v, fmt.Errorf("disagg: %w", err)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("disagg: %s: %w", path, err)
	}
	return v, nil
}

// readJSONL reads one JSON value per line.
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("disagg: %w", err)
	}
	defer f.Close()
	var out []T
	dec := json.NewDecoder(f)
	for dec.More() {
		var v T
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("disagg: %s, row %d: %w", path, len(out)+1, err)
		}
		out = append(out, v)
	}
	return out, nil
}
