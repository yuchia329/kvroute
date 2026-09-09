package bench_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
)

// The two thresholds are measured on two workload points, one each, because the
// generator will not let both pressures be high at once: concentrating the draws
// onto hot conversations means touching fewer distinct ones, so skew discounts
// working set. Each sweep therefore leaves the other condition off, and each is
// led by a spill-off reference at its own workload point.
func TestEachThresholdIsSweptAloneAgainstItsOwnReference(t *testing.T) {
	for name, sweep := range map[string][]policy.Spill{
		"kv":   bench.KVHighWaterSweep(),
		"load": bench.LoadImbalanceSweep(),
	} {
		if len(sweep) < 2 {
			t.Fatalf("%s sweep has %d points, want a reference and its levels", name, len(sweep))
		}
		if sweep[0].Enabled() {
			t.Errorf("%s sweep does not open on a spill-off reference: %v", name, sweep[0])
		}
		for _, p := range sweep[1:] {
			if err := p.Validate(); err != nil {
				t.Errorf("%s sweep holds a point the router would refuse: %v", name, err)
			}
			if !p.Enabled() {
				t.Errorf("%s sweep holds a point with no spill rule", name)
			}
			// The other condition is off, which is what makes the row
			// single-factor: a second live condition would move the result and
			// the table would credit the axis that happened to be swept.
			if name == "kv" && p.LoadImbalanceFactor != 0 {
				t.Errorf("the KV sweep leaves the load condition on at %v", p)
			}
			if name == "load" && p.KVHighWater != 0 {
				t.Errorf("the load sweep leaves the KV condition on at %v", p)
			}
		}
	}
}

// Each sweep covers its own axis exactly once.
func TestEachSweepCoversItsAxis(t *testing.T) {
	kv := bench.KVHighWaterSweep()
	if len(kv) != len(bench.KVHighWaterGrid)+1 {
		t.Errorf("the KV sweep has %d points, want %d levels plus a reference", len(kv), len(bench.KVHighWaterGrid))
	}
	load := bench.LoadImbalanceSweep()
	if len(load) != len(bench.LoadImbalanceGrid)+1 {
		t.Errorf("the load sweep has %d points, want %d levels plus a reference", len(load), len(bench.LoadImbalanceGrid))
	}
}

// The two points are the ones the generator's own discount permits: memory
// pressure needs an unconcentrated draw, and load imbalance needs a
// concentrated one. A pair that shared a skew would be measuring one live
// condition and one dormant one at both points.
func TestTheTwoWorkloadPointsSeparateTheTwoPressures(t *testing.T) {
	if bench.KVPressureWorkingSet <= bench.LoadImbalanceWorkingSet {
		t.Errorf("the KV point offers WS %v against the load point's %v, so it applies no more memory pressure",
			bench.KVPressureWorkingSet, bench.LoadImbalanceWorkingSet)
	}
	if bench.LoadImbalanceSkew <= bench.KVPressureSkew {
		t.Errorf("the load point runs at skew %v against the KV point's %v, so it concentrates no more traffic",
			bench.LoadImbalanceSkew, bench.KVPressureSkew)
	}
	if bench.KVPressureSkew != 0 {
		t.Errorf("the KV point runs at skew %v, which discounts the working set it exists to apply", bench.KVPressureSkew)
	}
}

// A grid point is a label a cell carries, so it has to survive the round trip
// through the spec a command takes it as.
func TestAGridPointSurvivesItsSpec(t *testing.T) {
	for _, want := range append(bench.KVHighWaterSweep(), bench.LoadImbalanceSweep()...) {
		got, err := bench.ParseSpill(bench.FormatSpill(want))
		if err != nil {
			t.Fatalf("ParseSpill(%q): %v", bench.FormatSpill(want), err)
		}
		if got != want {
			t.Errorf("%v round-tripped to %v", want, got)
		}
	}
}

func TestAnUnreadableGridPointIsRefused(t *testing.T) {
	for _, spec := range []string{"nonsense", "0.8", "0.8/2.0/3", "1.5/2.0", "0.8/0.5"} {
		if _, err := bench.ParseSpill(spec); err == nil {
			t.Errorf("%q was accepted as a grid point", spec)
		}
	}
}

// The same argument the policy check rests on. The router is started with its
// thresholds and the sweep is only told what they were, so nothing else stands
// between a mistyped flag and a grid of cells labelled with a point that never
// ran — nine cells of one point measured nine times, which no later analysis
// could detect.
func TestASweepRefusesARouterAtADifferentGridPoint(t *testing.T) {
	running := policy.Spill{KVHighWater: 0.80, LoadImbalanceFactor: 2.0}
	labelled := policy.Spill{KVHighWater: 0.90, LoadImbalanceFactor: 2.0}

	err := routerCheckUnderTest(t, policy.PrefixAffinityName, running, labelled)
	if err == nil {
		t.Fatal("a sweep was allowed to label its cells with a grid point the router is not running")
	}
	if !strings.Contains(err.Error(), "0.90") && !strings.Contains(err.Error(), "0.80") {
		t.Errorf("the refusal does not name the two points: %v", err)
	}
}

func TestASweepAtTheRoutersOwnGridPointIsAllowed(t *testing.T) {
	running := policy.Spill{KVHighWater: 0.80, LoadImbalanceFactor: 2.0}
	if err := routerCheckUnderTest(t, policy.PrefixAffinityName, running, running); err != nil {
		t.Fatalf("a matching grid point was refused: %v", err)
	}
}

// The three policies with no spill rule report no grid point, and a sweep of
// them must not have to name one.
func TestAPolicyWithNoSpillRuleNeedsNoGridPoint(t *testing.T) {
	if err := routerCheckUnderTest(t, policy.RoundRobinName, policy.Spill{}, policy.Spill{}); err != nil {
		t.Fatalf("a policy with no tunables was refused: %v", err)
	}
}

// routerCheckUnderTest starts a router at one grid point and asks a sweep
// labelled with another to run against it, returning what the sweep refused to
// do.
func routerCheckUnderTest(t *testing.T, policyName string, running, labelled policy.Spill) error {
	t.Helper()
	_, replicaURL := fakeReplicaServer(t)
	target := routerRunning(t, policyName, running, "replica-0="+replicaURL)

	_, err := bench.RunSweep(context.Background(), bench.SweepConfig{
		Dir:           t.TempDir(),
		Target:        target,
		Policy:        policyName,
		Spill:         labelled,
		Concurrencies: []int{1},
		Repetitions:   1,
		CellDuration:  20 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: "m", PromptBytes: 64, OutputTokens: 1}),
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return err
}

// routerRunning serves a router at a named policy and grid point.
func routerRunning(t *testing.T, policyName string, spill policy.Spill, specs ...string) string {
	t.Helper()
	replicas, err := fleet.ParseSpecs(specs)
	if err != nil {
		t.Fatalf("parse replica specs: %v", err)
	}
	f, err := fleet.New(replicas)
	if err != nil {
		t.Fatalf("new fleet: %v", err)
	}
	index, err := prefix.New(prefix.Config{NodeCap: 4096, TTL: time.Hour})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	chosen, err := policy.ByName(policyName, policy.Options{PrefixIndex: index, Spill: spill})
	if err != nil {
		t.Fatalf("policy.ByName: %v", err)
	}
	rt, err := router.New(router.Config{
		Fleet:   f,
		Policy:  chosen,
		Records: record.NewWriter[record.Request](nopWriter{}),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}
