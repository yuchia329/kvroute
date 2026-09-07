package characterize_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/gpu"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
	"github.com/yuchia329/kvroute/internal/record"
)

func hostTopology(t *testing.T) gpu.Topology {
	t.Helper()
	topo, err := gpu.NewProber(gputest.Runner("", "", "")).Topology(context.Background())
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	return topo
}

func sixReplicaIDs() []string {
	return []string{"replica-0", "replica-1", "replica-2", "replica-3", "replica-4", "replica-5"}
}

// replicaRows builds n successful rows for one replica at one load level.
func replicaRows(replica string, concurrency, n int, ttft, itl time.Duration) []bench.Result {
	out := make([]bench.Result, 0, n)
	for i := range n {
		out = append(out, bench.Result{
			Labels:      bench.Labels{Concurrency: concurrency},
			Replica:     replica,
			StartedAtNs: int64(i) * int64(time.Second),
			TTFTNs:      ttft.Nanoseconds(),
			ITLP50Ns:    itl.Nanoseconds(),
			TotalNs:     (ttft + 64*itl).Nanoseconds(),
			Outcome:     record.OutcomeSuccess,
		})
	}
	return out
}

// evenFleet builds rows for six replicas whose latency differs by at most
// nudge, at both load levels, over two repetitions that agree with each other.
func evenFleet(base time.Duration, nudges ...time.Duration) []bench.Result {
	var rows []bench.Result
	for i, id := range sixReplicaIDs() {
		nudge := time.Duration(0)
		if i < len(nudges) {
			nudge = nudges[i]
		}
		for repetition := 1; repetition <= 2; repetition++ {
			rows = append(rows, repeated(replicaRows(id, 1, 20, base+nudge, 8*time.Millisecond), repetition)...)
			rows = append(rows, repeated(replicaRows(id, 16, 20, 2*(base+nudge), 9*time.Millisecond), repetition)...)
		}
	}
	return rows
}

// jittered spreads a probe's TTFTs evenly across a window around their median,
// so pooling two probes behaves the way pooling two real ones does.
func jittered(rows []bench.Result, window time.Duration) []bench.Result {
	for i := range rows {
		offset := window/2 - time.Duration(i)*window/time.Duration(len(rows))
		rows[i].TTFTNs += offset.Nanoseconds()
	}
	return rows
}

// repeated stamps rows with a repetition, so a level has more than one and its
// own noise floor can be estimated.
func repeated(rows []bench.Result, repetition int) []bench.Result {
	for i := range rows {
		rows[i].Repetition = repetition
	}
	return rows
}

// The replica's own id carries the GPU index because ops/replica.sh assigns
// both, which is what lets a latency difference be read against the host.
func TestPlacementsPairEachReplicaWithItsCardsNUMANode(t *testing.T) {
	placements := characterize.Placements(sixReplicaIDs(), hostTopology(t))

	if len(placements) != 6 {
		t.Fatalf("got %d placements, want 6", len(placements))
	}
	if placements[0].GPUIndex != 0 || placements[0].NUMANode != 0 {
		t.Errorf("replica-0 is %+v, want GPU 0 on node 0", placements[0])
	}
	if placements[4].GPUIndex != 4 || placements[4].NUMANode != 1 {
		t.Errorf("replica-4 is %+v, want GPU 4 on node 1", placements[4])
	}
}

func TestASymmetricFleetPassesAtEveryLevelAndNeedsNoEscalation(t *testing.T) {
	s := characterize.CompareReplicas(evenFleet(320*time.Millisecond, 0, time.Millisecond, 0, 2*time.Millisecond, 0, time.Millisecond),
		characterize.Placements(sixReplicaIDs(), hostTopology(t)), hostTopology(t), characterize.DefaultSymmetryTolerance)

	if !s.Symmetric {
		t.Fatalf("an even fleet read as asymmetric: %v", s.Findings)
	}
	if s.Escalation != characterize.EscalationNone {
		t.Errorf("escalation = %s, want none", s.Escalation)
	}
	if len(s.Levels) != 2 {
		t.Fatalf("compared %d levels, want both of them", len(s.Levels))
	}
	if s.Levels[0].Concurrency != 1 || s.Levels[1].Concurrency != 16 {
		t.Errorf("levels are %d and %d, want them in load order", s.Levels[0].Concurrency, s.Levels[1].Concurrency)
	}
	if len(s.Levels[0].Replicas) != 6 {
		t.Errorf("level 1 compared %d replicas, want 6", len(s.Levels[0].Replicas))
	}
}

// The specific failure idea.md predicts: the four cards on NUMA 0 get half the
// threads the two on NUMA 1 do, so they fall behind. The finding has to say
// that the difference follows the node boundary, because that is what makes
// pinning the fix rather than a guess.
func TestAFleetSlowOnOneNUMANodeIsCaughtAndPointsAtPinning(t *testing.T) {
	slow := 60 * time.Millisecond
	rows := evenFleet(320*time.Millisecond, slow, slow, slow, slow, 0, 0)

	s := characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Symmetric {
		t.Fatal("a fleet where four replicas are 19% slower read as symmetric")
	}
	if !s.Resolved {
		t.Error("a difference every repetition agreed on was reported as unresolvable")
	}
	if s.Escalation != characterize.EscalationPinCPUs {
		t.Errorf("escalation = %s, want pin_cpus: the first rung of §10's ladder", s.Escalation)
	}
	level := s.Levels[0]
	if level.Slowest == level.Fastest {
		t.Error("the comparison named the same replica fastest and slowest")
	}
	if level.NUMASpread <= characterize.DefaultSymmetryTolerance {
		t.Errorf("NUMA spread = %.3f, want it over the tolerance: the difference follows the node boundary", level.NUMASpread)
	}
	if len(level.NUMA) != 2 || level.NUMA[0].ThreadsEach != 12 || level.NUMA[1].ThreadsEach != 24 {
		t.Errorf("NUMA groups are %+v, want the host's 12 and 24 threads per GPU", level.NUMA)
	}
	var sawBoundary bool
	for _, finding := range s.Findings {
		if strings.Contains(finding, "NUMA boundary") {
			sawBoundary = true
		}
	}
	if !sawBoundary {
		t.Errorf("findings %v never say the difference follows the NUMA boundary", s.Findings)
	}
}

// Inter-token latency is checked on its own: a fleet even on TTFT can still be
// uneven once it is generating, and that is the dimension the SLO's second half
// is about.
func TestASpreadInInterTokenLatencyAloneIsStillAsymmetry(t *testing.T) {
	var rows []bench.Result
	for i, id := range sixReplicaIDs() {
		itl := 8 * time.Millisecond
		if i >= 4 {
			itl = 12 * time.Millisecond
		}
		for repetition := 1; repetition <= 2; repetition++ {
			rows = append(rows, repeated(replicaRows(id, 1, 20, 320*time.Millisecond, itl), repetition)...)
		}
	}

	s := characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Symmetric {
		t.Fatal("a 50% inter-token spread read as symmetric because TTFT was even")
	}
}

// "Nothing was compared" and "everything matched" must not read the same.
func TestAnUnmeasuredFleetDoesNotClaimToBeSymmetric(t *testing.T) {
	s := characterize.CompareReplicas(nil, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Symmetric {
		t.Fatal("a fleet that was never driven claimed to be symmetric")
	}
	if len(s.Findings) == 0 {
		t.Error("an unproven fleet gave no reason")
	}
}

// A run that compared nothing has nothing to escalate. Recommending CPU pinning
// off a fleet nobody looked at is advice with no measurement behind it.
func TestAnUnmeasuredFleetRecommendsNoEscalation(t *testing.T) {
	s := characterize.CompareReplicas(nil, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Escalation != characterize.EscalationNone {
		t.Errorf("escalation = %s, want none: nothing was compared", s.Escalation)
	}
}

// The measurement this ticket actually produced: at concurrency 16 the replicas
// differed by 30% and each one differed from itself between repetitions by up
// to 44%. Acting on that would mean pinning CPUs — a change to the pinned
// engine configuration every later cell depends on — on the strength of noise.
func TestASpreadInsideTheMeasurementsOwnNoiseIsNotAnAsymmetry(t *testing.T) {
	slow := map[string]map[int]time.Duration{
		// Each replica is fast in one repetition and slow in the other, so the
		// pooled medians differ while nothing about a replica does.
		"replica-0": {1: 1030 * time.Millisecond, 2: 1315 * time.Millisecond},
		"replica-1": {1: 1122 * time.Millisecond, 2: 854 * time.Millisecond},
		"replica-2": {1: 997 * time.Millisecond, 2: 1292 * time.Millisecond},
		"replica-3": {1: 1305 * time.Millisecond, 2: 1249 * time.Millisecond},
		"replica-4": {1: 903 * time.Millisecond, 2: 1298 * time.Millisecond},
		"replica-5": {1: 1020 * time.Millisecond, 2: 1228 * time.Millisecond},
	}
	var rows []bench.Result
	for _, id := range sixReplicaIDs() {
		for repetition, ttft := range slow[id] {
			// Spread around the median rather than pinned to it: a probe at
			// concurrency 16 produces a wide distribution, and pooling two
			// perfectly flat ones would give a median that is an artefact of
			// the fixture rather than of the data.
			rows = append(rows, repeated(jittered(replicaRows(id, 16, 40, ttft, 13*time.Millisecond), 300*time.Millisecond), repetition)...)
		}
	}

	s := characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Resolved {
		t.Error("a spread smaller than the measurement's own noise was reported as resolvable")
	}
	if s.Escalation != characterize.EscalationNone {
		t.Errorf("escalation = %s, want none: pinning CPUs on the strength of noise changes the engine configuration every later cell depends on", s.Escalation)
	}
	level := s.Levels[0]
	if level.RepeatSpread <= level.TTFTSpread {
		t.Errorf("repeat spread %.2f is not above the between-replica spread %.2f, so this fixture does not test what it claims",
			level.RepeatSpread, level.TTFTSpread)
	}
	var sawNoise bool
	for _, finding := range s.Findings {
		if strings.Contains(finding, "against itself") {
			sawNoise = true
		}
	}
	if !sawNoise {
		t.Errorf("findings %v never say the spread is inside the measurement's own noise", s.Findings)
	}
}

// With one repetition there is nothing to estimate the noise from, and a level
// that cannot estimate it must not silently claim its spread is resolvable...
// nor must it refuse a difference that is plainly there.
func TestASingleRepetitionHasNoNoiseFloorAndSaysSoByNotClaimingOne(t *testing.T) {
	var rows []bench.Result
	for i, id := range sixReplicaIDs() {
		ttft := 320 * time.Millisecond
		if i == 3 {
			ttft = 400 * time.Millisecond
		}
		rows = append(rows, replicaRows(id, 1, 20, ttft, 8*time.Millisecond)...)
	}

	s := characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Levels[0].RepeatSpread >= 0 {
		t.Errorf("repeat spread = %.2f from a single repetition, want it recorded as unknown", s.Levels[0].RepeatSpread)
	}
	if s.Symmetric {
		t.Error("a 25% difference was not reported")
	}
	// No estimate of the noise is not an estimate of zero. Treating it as one
	// would make the run with the least evidence the most confident, and it
	// would pin CPUs off a single probe's median.
	if s.Resolved {
		t.Error("a single repetition claimed it could tell the difference from noise")
	}
	if s.Escalation != characterize.EscalationNone {
		t.Errorf("escalation = %s, want none: nothing estimates this measurement's noise", s.Escalation)
	}
}

// A replica that answered nothing leaves a hole in the comparison. Reporting
// the remaining five as evenly matched would be a symmetry verdict built on the
// absence of the replica most likely to be the problem.
func TestAReplicaThatAnsweredNothingIsNotAPerfectlyEvenFleet(t *testing.T) {
	var rows []bench.Result
	for i, id := range sixReplicaIDs() {
		for repetition := 1; repetition <= 2; repetition++ {
			batch := replicaRows(id, 1, 20, 320*time.Millisecond, 8*time.Millisecond)
			if i == 2 {
				// Dispatched, never answered: no latency to contribute.
				for j := range batch {
					batch[j].Outcome = record.OutcomeDropped
					batch[j].TTFTNs, batch[j].ITLP50Ns = 0, 0
				}
			}
			rows = append(rows, repeated(batch, repetition)...)
		}
	}

	s := characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if s.Symmetric {
		t.Fatal("a fleet with a silent replica reported itself interchangeable")
	}
	if s.Resolved {
		t.Error("a comparison with a hole in it claimed to have resolved something")
	}
	if s.Escalation != characterize.EscalationNone {
		t.Errorf("escalation = %s, want none: the fix for a replica that answers nothing is not CPU pinning", s.Escalation)
	}
	if got := s.Levels[0].Silent; len(got) != 1 || got[0] != "replica-2" {
		t.Errorf("silent replicas = %v, want [replica-2]", got)
	}
}

// The cleanest result the fleet produced, and the one the first version of this
// logic threw away: a 2.3% spread against a 2.8% noise floor, both far inside
// the 8% tolerance. The measurement cannot say which replica is faster and does
// not need to — an 8% difference is ruled out either way, and reporting that as
// "unresolved" would file the best evidence in the project under "nothing was
// learned".
func TestASpreadAndItsNoiseBothInsideToleranceSettleTheQuestion(t *testing.T) {
	var rows []bench.Result
	ttft := map[string]map[int]time.Duration{
		"replica-0": {1: 332 * time.Millisecond, 2: 330 * time.Millisecond, 3: 334 * time.Millisecond},
		"replica-1": {1: 329 * time.Millisecond, 2: 331 * time.Millisecond, 3: 327 * time.Millisecond},
		"replica-2": {1: 324 * time.Millisecond, 2: 326 * time.Millisecond, 3: 325 * time.Millisecond},
		"replica-3": {1: 330 * time.Millisecond, 2: 328 * time.Millisecond, 3: 332 * time.Millisecond},
		"replica-4": {1: 331 * time.Millisecond, 2: 333 * time.Millisecond, 3: 329 * time.Millisecond},
		"replica-5": {1: 327 * time.Millisecond, 2: 325 * time.Millisecond, 3: 329 * time.Millisecond},
	}
	for _, id := range sixReplicaIDs() {
		for repetition, t := range ttft[id] {
			rows = append(rows, repeated(replicaRows(id, 1, 40, t, 8*time.Millisecond), repetition)...)
		}
	}

	s := characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), hostTopology(t)),
		hostTopology(t), characterize.DefaultSymmetryTolerance)

	if !s.Symmetric {
		t.Fatalf("a fleet even to 2%% read as asymmetric: %v", s.Findings)
	}
	if !s.Resolved {
		t.Errorf("a measurement precise to %.1f%%, against an %.0f%% tolerance, reported that it had settled nothing",
			s.Levels[0].RepeatSpread*100, s.Tolerance*100)
	}
	if s.Levels[0].RepeatSpread > characterize.DefaultSymmetryTolerance {
		t.Fatalf("this fixture's noise floor %.3f is not inside the tolerance, so it does not test what it claims",
			s.Levels[0].RepeatSpread)
	}
}
