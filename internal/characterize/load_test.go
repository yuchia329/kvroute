package characterize_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
)

// soundFloor is a floor an SLO can honestly be derived from: enough successful
// requests, real timings, unflagged, and measured at a prefix-cache hit rate low
// enough that it is a prefill cost rather than a cache lookup.
func soundFloor() characterize.Floor {
	return characterize.Floor{
		Concurrency:      1,
		Replicas:         6,
		MaxPrefixHitRate: 0.05,
		PrefixCache:      characterize.PrefixCacheDelta{Hits: 1, Queries: 1000, Read: true},
		Summary: bench.Summary{
			Requests:  1487,
			Successes: 1487,
			TTFTP50Ns: (329 * time.Millisecond).Nanoseconds(),
			ITLP50Ns:  (7800 * time.Microsecond).Nanoseconds(),
		},
	}
}

func recordAt(t *testing.T, dir string, c characterize.Characterization) string {
	t.Helper()
	contents, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, characterize.RecordName)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestTheSLOReachesASweepAsTheRecordRatherThanAsRetypedFlags. The SLO is derived
// from a measured floor, and a threshold retyped by hand is one that can differ
// from the one that was derived — which would leave a goodput figure internally
// consistent and quietly wrong.
func TestTheSLOReachesASweepAsTheRecordRatherThanAsRetypedFlags(t *testing.T) {
	floor := soundFloor()
	want := floor.Derive(characterize.DefaultSLOMultiple)
	dir := t.TempDir()
	path := recordAt(t, dir, characterize.Characterization{Floor: floor, SLO: want})

	for _, from := range []string{dir, path} {
		got, err := characterize.Load(from)
		if err != nil {
			t.Fatalf("load %s: %v", from, err)
		}
		if ok, why := got.SLOUsable(); !ok {
			t.Fatalf("a sound floor's SLO was refused: %s", why)
		}
		if got.SLO.SLO() != (bench.SLO{TTFT: want.TTFT, ITL: want.ITL}) {
			t.Errorf("SLO = %+v, want %+v", got.SLO.SLO(), want)
		}
	}
}

func TestLoadingSomethingThatIsNotACharacterizationSaysSo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, characterize.RecordName)
	if err := os.WriteFile(path, []byte(`{"floor": `), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := characterize.Load(dir)
	if err == nil {
		t.Fatal("a truncated record loaded without complaint")
	}
	if !strings.Contains(err.Error(), "not a characterization record") {
		t.Errorf("err = %v, want it to name what it could not read", err)
	}
}

// TestAnSLODerivedFromAnUnusableFloorIsRefused: every cell of every sweep would be
// judged against it, and none of them would record that its derivation was in
// doubt.
func TestAnSLODerivedFromAnUnusableFloorIsRefused(t *testing.T) {
	thin := soundFloor()
	thin.Successes = 3
	thin.Requests = 3
	c := characterize.Characterization{Floor: thin, SLO: thin.Derive(characterize.DefaultSLOMultiple)}

	ok, why := c.SLOUsable()
	if ok {
		t.Fatal("an SLO derived from three requests was accepted")
	}
	if !strings.Contains(why, "floor") {
		t.Errorf("why = %q, want it to name the floor", why)
	}
}

// TestAFlagAboutTheFleetDoesNotDisqualifyTheThreshold is the judgement this method
// exists to make. The symmetry verdict is a claim about whether the replicas are
// interchangeable and an under-warmed probe at the upper load level is about that
// probe; neither is a fault in a floor measured at concurrency one. Refusing the
// SLO for them would block a sweep over a figure the SLO does not rest on — which
// is exactly the state the fleet's own recorded characterization is in.
func TestAFlagAboutTheFleetDoesNotDisqualifyTheThreshold(t *testing.T) {
	c := characterize.Characterization{
		Floor:       soundFloor(),
		SLO:         soundFloor().Derive(characterize.DefaultSLOMultiple),
		Symmetry:    characterize.Symmetry{Symmetric: false},
		Flagged:     true,
		FlagReasons: []string{"replica symmetry is unresolved at concurrency 32"},
	}

	if ok, why := c.SLOUsable(); !ok {
		t.Errorf("the SLO was refused over a flag about the fleet rather than the floor: %s", why)
	}
	// The caller still has to be able to say so, which is what the reasons are for.
	if len(c.FlagReasons) == 0 || c.Symmetry.Symmetric {
		t.Error("the record no longer carries what a caller must warn about")
	}
}

// TestARecordWithNoDerivedSLOIsRefused: a zero threshold is not a lenient one, it
// is no threshold, and goodput against it would be throughput.
func TestARecordWithNoDerivedSLOIsRefused(t *testing.T) {
	c := characterize.Characterization{Floor: soundFloor()}

	ok, why := c.SLOUsable()
	if ok {
		t.Fatal("a record with no derived SLO was accepted")
	}
	if !strings.Contains(why, "no derived SLO") {
		t.Errorf("why = %q, want it to say the record carries no SLO", why)
	}
}
