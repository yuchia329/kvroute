package characterize_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/gpu"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

// sixFakes stands up six fake replicas, the nth of them slowed by slow[n].
func sixFakes(t *testing.T, slow map[int]time.Duration) []fleet.Replica {
	t.Helper()
	replicas, _ := sixFakesRecordingBodies(t, slow)
	return replicas
}

// bodies records every request body a fleet was sent, so a test can ask whether
// any prompt was ever sent twice.
type bodies struct {
	mu   sync.Mutex
	seen map[string]int
}

func (b *bodies) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			b.mu.Lock()
			b.seen[string(body)]++
			b.mu.Unlock()
		}
		h.ServeHTTP(w, r)
	})
}

func sixFakesRecordingBodies(t *testing.T, slow map[int]time.Duration) ([]fleet.Replica, *bodies) {
	t.Helper()
	sent := &bodies{seen: map[string]int{}}
	var replicas []fleet.Replica
	for i := range 6 {
		srv := httptest.NewServer(sent.wrap(fakereplica.New(fakereplica.Config{
			ID:           replicaName(i),
			Model:        testModel,
			TTFT:         5*time.Millisecond + slow[i],
			InterToken:   time.Millisecond,
			OutputTokens: 4,
			NumGPUBlocks: 7872,
			BlockSize:    16,
		}).Handler()))
		t.Cleanup(srv.Close)
		replicas = append(replicas, fleet.Replica{ID: replicaName(i), BaseURL: srv.URL})
	}
	return replicas, sent
}

func replicaName(i int) string { return "replica-" + string(rune('0'+i)) }

const testModel = "test-model"

func runCharacterization(t *testing.T, replicas []fleet.Replica, levels []int) (characterize.Characterization, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := characterize.Run(context.Background(), characterize.Config{
		Dir:           dir,
		Replicas:      replicas,
		Model:         testModel,
		Levels:        levels,
		Repetitions:   2,
		ProbeDuration: 120 * time.Millisecond,
		ReplicaWarmup: 1,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 4}),
		Prober:        gpu.NewProber(gputest.Runner(gputest.SixIdleDevices, gputest.NoComputeApps, "")),
		Log:           slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	return c, dir
}

// One pass, three answers: the capacity, the floor the SLO comes from, and the
// symmetry verdict, all about the same fleet in the same state.
func TestARunEstablishesCapacityFloorSLOAndSymmetryTogether(t *testing.T) {
	c, dir := runCharacterization(t, sixFakes(t, nil), []int{1, 4})

	if c.Capacity.Tokens != 6*7872*16 {
		t.Errorf("fleet capacity = %d tokens, want the six replicas summed", c.Capacity.Tokens)
	}
	if len(c.WorkingSets) != len(characterize.WorkingSetPoints) {
		t.Errorf("rescaled %d WS points, want %d", len(c.WorkingSets), len(characterize.WorkingSetPoints))
	}
	if len(c.Topology.GPUs) != 6 {
		t.Errorf("recorded %d GPUs of topology, want 6", len(c.Topology.GPUs))
	}
	if c.Floor.Concurrency != 1 || c.Floor.Successes == 0 {
		t.Errorf("floor is %+v, want it measured at concurrency 1", c.Floor)
	}
	if c.SLO.TTFT <= c.Floor.TTFT() {
		t.Errorf("SLO TTFT %v is not above the floor %v it was derived from", c.SLO.TTFT, c.Floor.TTFT())
	}
	if c.SLO.Multiple != characterize.DefaultSLOMultiple {
		t.Errorf("SLO multiple = %g, want the default %g stated", c.SLO.Multiple, characterize.DefaultSLOMultiple)
	}
	if len(c.Symmetry.Levels) != 2 {
		t.Fatalf("compared %d levels, want both", len(c.Symmetry.Levels))
	}

	// Six replicas at two levels, twice each.
	if len(c.Probes) != 6*2*2 {
		t.Errorf("ran %d probes, want %d", len(c.Probes), 6*2*2)
	}
	// The rows are the system of record, and the record is on disk.
	rows, err := os.ReadFile(filepath.Join(dir, "probes.jsonl"))
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) == 0 {
		t.Error("no per-request rows were written")
	}
	written, err := os.ReadFile(filepath.Join(dir, "characterization.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var reread characterize.Characterization
	if err := json.Unmarshal(written, &reread); err != nil {
		t.Fatalf("the record is not readable back: %v", err)
	}
	if reread.Capacity.Tokens != c.Capacity.Tokens {
		t.Error("the record on disk disagrees with what the run returned")
	}
}

// Driving one replica at a time puts the same replica first every pass, so a
// host that drifts would read as a fleet ordered by index. Alternating the
// order is what keeps that from happening, and it only works if it happens.
func TestTheReplicaOrderAlternatesBetweenRepetitions(t *testing.T) {
	c, _ := runCharacterization(t, sixFakes(t, nil), []int{1})

	first := map[int]string{}
	for _, probe := range c.Probes {
		if _, seen := first[probe.Repetition]; !seen {
			first[probe.Repetition] = probe.ReplicaID
		}
	}
	if len(first) != 2 {
		t.Fatalf("ran %d repetitions, want 2", len(first))
	}
	if first[1] == first[2] {
		t.Errorf("both repetitions started with %s, so a drifting host would look like a slow replica", first[1])
	}
}

// A slow replica has to reach the verdict, and the verdict has to reach the
// characterization's own flags: every downstream figure rests on these numbers.
func TestASlowReplicaFlagsTheWholeCharacterization(t *testing.T) {
	c, _ := runCharacterization(t, sixFakes(t, map[int]time.Duration{3: 20 * time.Millisecond}), []int{1})

	if c.Symmetry.Symmetric {
		t.Fatal("a replica four times slower than the others read as interchangeable")
	}
	if c.Symmetry.Escalation != characterize.EscalationPinCPUs {
		t.Errorf("escalation = %s, want pin_cpus", c.Symmetry.Escalation)
	}
	if !c.Flagged {
		t.Error("an asymmetric fleet did not flag the characterization it belongs to")
	}
}

func TestTheReportStatesEveryFactTheRunEstablished(t *testing.T) {
	c, _ := runCharacterization(t, sixFakes(t, nil), []int{1, 4})

	report := characterize.Report(c)
	for _, want := range []string{
		"Aggregate fleet KV capacity",
		"num_gpu_blocks",
		"Working set ratios rescaled",
		"Host GPU topology",
		"Hardware latency floor",
		"the measured floor",
		"Replica symmetry",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report never mentions %q", want)
		}
	}
	// The threshold never appears without the floor it was derived from.
	if !strings.Contains(report, c.SLO.String()) {
		t.Error("the report states an SLO without its derivation")
	}
}

// The failure that cost a whole measured run: every probe sent the same prompts,
// so the second repetition read them back out of the replicas' prefix caches and
// reported 46 ms where the hardware takes 325 ms. Nothing about the latency says
// which happened, so the bytes have to be different by construction.
func TestNoTwoProbesEverSendTheSameBytes(t *testing.T) {
	dir := t.TempDir()
	replicas, sent := sixFakesRecordingBodies(t, nil)
	if _, err := characterize.Run(context.Background(), characterize.Config{
		Dir:           dir,
		Replicas:      replicas,
		Model:         testModel,
		Levels:        []int{1, 4},
		Repetitions:   2,
		ProbeDuration: 100 * time.Millisecond,
		ReplicaWarmup: 2,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 2}),
		Prober:        gpu.NewProber(gputest.Runner(gputest.SixIdleDevices, gputest.NoComputeApps, "")),
		Log:           slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatalf("characterize: %v", err)
	}

	sent.mu.Lock()
	defer sent.mu.Unlock()
	repeats, total := 0, 0
	for _, count := range sent.seen {
		total += count
		if count > 1 {
			repeats += count - 1
		}
	}
	if total == 0 {
		t.Fatal("no requests were sent")
	}
	if repeats > 0 {
		t.Errorf("%d of %d requests re-sent a prompt the fleet had already been given, so those measured the prefix cache rather than the hardware",
			repeats, total)
	}
}

// This pass does not resume, so a second run into the same directory would
// interleave two fleets' rows under one record.
func TestARunRefusesToAppendToAnEarlierRunsRows(t *testing.T) {
	dir := t.TempDir()
	replicas := sixFakes(t, nil)
	cfg := characterize.Config{
		Dir:           dir,
		Replicas:      replicas,
		Model:         testModel,
		Levels:        []int{1},
		ProbeDuration: 60 * time.Millisecond,
		Workload:      bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 2}),
		Log:           slog.New(slog.DiscardHandler),
	}
	if _, err := characterize.Run(context.Background(), cfg); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := characterize.Run(context.Background(), cfg); err == nil {
		t.Fatal("a second run appended to the first run's rows")
	}
}

// A fleet that cannot be reached is not a fleet with unknown symmetry: it is a
// run that must not produce a record at all.
func TestARunRefusesAFleetThatIsNotUp(t *testing.T) {
	srv := httptest.NewServer(fakereplica.New(fakereplica.Config{}).Handler())
	srv.Close()

	_, err := characterize.Run(context.Background(), characterize.Config{
		Dir:      t.TempDir(),
		Replicas: []fleet.Replica{{ID: "replica-0", BaseURL: srv.URL}},
		Model:    testModel,
		Log:      slog.New(slog.DiscardHandler),
	})
	if err == nil {
		t.Fatal("characterized a fleet that was not running")
	}
}

// The whole point of the together schedule. §10's hypothesis is that four cards
// sharing a NUMA node get a quarter of its threads each, and that ratio only
// exists while all four are busy — so if these probes do not actually overlap,
// the experiment measures the same thing the solo pass already did.
func TestTheTogetherScheduleRunsEveryReplicaAtTheSameTime(t *testing.T) {
	c, _ := runWithSchedule(t, characterize.ScheduleTogether, []int{2})

	if len(c.Probes) != 6 {
		t.Fatalf("ran %d probes, want one per replica", len(c.Probes))
	}
	// Every probe must overlap every other: one window, six replicas in it.
	for i, a := range c.Probes {
		if a.Schedule != characterize.ScheduleTogether {
			t.Errorf("%s records schedule %q", a.ID, a.Schedule)
		}
		for _, b := range c.Probes[i+1:] {
			if a.EndedAtNs <= b.StartedAtNs || b.EndedAtNs <= a.StartedAtNs {
				t.Errorf("%s and %s did not overlap, so nothing was contended for", a.ID, b.ID)
			}
		}
	}
	if c.Schedule != characterize.ScheduleTogether {
		t.Errorf("the record says schedule %q", c.Schedule)
	}
}

// The solo schedule has to keep its guarantee, which is the opposite one.
func TestTheSoloScheduleStillLeavesEveryOtherReplicaIdle(t *testing.T) {
	c, _ := runWithSchedule(t, characterize.ScheduleSolo, []int{2})

	for i, a := range c.Probes {
		for _, b := range c.Probes[i+1:] {
			if a.EndedAtNs > b.StartedAtNs && b.EndedAtNs > a.StartedAtNs {
				t.Errorf("%s and %s overlapped, so neither had the host to itself", a.ID, b.ID)
			}
		}
	}
}

// The measurement gap the first pass left: the driver knew how many requests it
// held, and nothing recorded what the engine actually batched.
func TestEveryProbeRecordsWhatTheEngineWasActuallyRunning(t *testing.T) {
	c, _ := runWithSchedule(t, characterize.ScheduleTogether, []int{4})

	for _, probe := range c.Probes {
		if !probe.EngineLoad.Read {
			t.Fatalf("%s recorded no engine load, so its batch is unknown", probe.ID)
		}
		if probe.EngineLoad.Samples == 0 {
			t.Errorf("%s took no samples", probe.ID)
		}
		// The fake serves everything it accepts, so its running count tracks
		// what the driver offered. A real engine splits running from waiting at
		// its batch size, which is the number this exists to capture.
		if probe.EngineLoad.MaxRunning <= 0 {
			t.Errorf("%s never saw the replica running anything, over %d samples",
				probe.ID, probe.EngineLoad.Samples)
		}
		if probe.EngineLoad.MaxRunning > float64(probe.Concurrency) {
			t.Errorf("%s saw %.0f running, more than the %d the driver held",
				probe.ID, probe.EngineLoad.MaxRunning, probe.Concurrency)
		}
	}
}

func runWithSchedule(t *testing.T, schedule characterize.Schedule, levels []int) (characterize.Characterization, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := characterize.Run(context.Background(), characterize.Config{
		Dir:                  dir,
		Replicas:             sixFakes(t, nil),
		Model:                testModel,
		Schedule:             schedule,
		Levels:               levels,
		Repetitions:          1,
		ProbeDuration:        250 * time.Millisecond,
		EngineSampleInterval: 10 * time.Millisecond,
		ReplicaWarmup:        1,
		Workload:             bench.NewFixedWorkload(bench.FixedWorkload{Model: testModel, PromptBytes: 64, OutputTokens: 3}),
		Prober:               gpu.NewProber(gputest.Runner(gputest.SixIdleDevices, gputest.NoComputeApps, "")),
		Log:                  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("characterize: %v", err)
	}
	return c, dir
}
