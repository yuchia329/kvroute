package bench_test

import (
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/record"
)

// decided builds a successful result carrying one routing decision.
func decided(startedAt time.Time, reason policy.Reason) bench.Result {
	r := success(startedAt, time.Second)
	r.Decision = string(reason)
	return r
}

// The mix is what says whether a grid point's goodput came from taking
// affinity or from declining it. Without the counts, two thresholds producing
// the same goodput are indistinguishable, and choosing between them is
// guesswork.
func TestTheDecisionMixIsCountedPerReason(t *testing.T) {
	start := time.Unix(1757000000, 0)
	got := bench.Summarize([]bench.Result{
		decided(start, policy.ReasonPrefixAffinity),
		decided(start, policy.ReasonPrefixAffinity),
		decided(start, policy.ReasonPrefixAffinity),
		decided(start, policy.ReasonCold),
		decided(start, policy.ReasonSpillHitRate),
		decided(start, policy.ReasonSpillLoad),
		decided(start, policy.ReasonSpillLoad),
		decided(start, policy.ReasonPromptUntokenized),
	}, bench.SummaryOptions{SLO: slo}).Decisions

	if got.PrefixAffinity != 3 {
		t.Errorf("affinity = %d, want 3", got.PrefixAffinity)
	}
	// Exact residency's own reason: a request it could not look up. Counted in
	// its own column rather than as undecided, which would read as version skew
	// between the router and the harness, or as cold, which would read as an
	// index that found nothing.
	if got.PromptUntokenized != 1 {
		t.Errorf("untokenized = %d, want 1", got.PromptUntokenized)
	}
	if got.Undecided != 0 {
		t.Errorf("undecided = %d, want 0: every reason here is one the router emits", got.Undecided)
	}
	if got.Cold != 1 {
		t.Errorf("cold = %d, want 1", got.Cold)
	}
	if got.SpillHitRate != 1 || got.SpillLoad != 2 {
		t.Errorf("spills = %d KV and %d load, want 1 and 2", got.SpillHitRate, got.SpillLoad)
	}
	if got.Spilled() != 3 {
		t.Errorf("Spilled() = %d, want 3", got.Spilled())
	}
	if got.Total() != 8 {
		t.Errorf("Total() = %d, want 8", got.Total())
	}
}

// The two spill conditions stay two columns all the way to the table. Collapsing
// them here would undo in the summary what the policy took two reasons to keep
// apart, and the pressure grid crosses two axes precisely to fire them
// separately.
func TestTheSummaryDoesNotCollapseTheTwoSpillConditions(t *testing.T) {
	start := time.Unix(1757000000, 0)
	kv := bench.Summarize([]bench.Result{decided(start, policy.ReasonSpillHitRate)}, bench.SummaryOptions{}).Decisions
	load := bench.Summarize([]bench.Result{decided(start, policy.ReasonSpillLoad)}, bench.SummaryOptions{}).Decisions

	if kv.SpillHitRate != 1 || kv.SpillLoad != 0 {
		t.Errorf("a KV spill counted as %+v", kv)
	}
	if load.SpillLoad != 1 || load.SpillHitRate != 0 {
		t.Errorf("a load spill counted as %+v", load)
	}
}

// Warm-up rows are excluded from the mix as they are from every other count.
// A cell whose warm-up ran cold and whose measured window ran warm would
// otherwise report an affinity rate that describes neither.
func TestTheMixExcludesWarmupAsEveryOtherCountDoes(t *testing.T) {
	start := time.Unix(1757000000, 0)
	warm := decided(start, policy.ReasonCold)
	warm.Warmup = true

	got := bench.Summarize([]bench.Result{warm, decided(start, policy.ReasonPrefixAffinity)},
		bench.SummaryOptions{SLO: slo}).Decisions

	if got.Cold != 0 {
		t.Errorf("a warm-up row was counted into the mix: %+v", got)
	}
	if got.Total() != 1 {
		t.Errorf("Total() = %d, want 1", got.Total())
	}
}

// A request the router never placed carries no decision. It is counted as
// undecided rather than dropped from the mix, so the mix's total can be
// reconciled against the cell's request count instead of quietly disagreeing
// with it.
func TestARequestWithNoDecisionIsCountedAsUndecided(t *testing.T) {
	start := time.Unix(1757000000, 0)
	got := bench.Summarize([]bench.Result{
		outcome(start, record.OutcomeDropped),
		decided(start, policy.ReasonPrefixAffinity),
	}, bench.SummaryOptions{SLO: slo}).Decisions

	if got.Undecided != 1 {
		t.Errorf("undecided = %d, want 1", got.Undecided)
	}
	if got.Total() != 2 {
		t.Errorf("Total() = %d, want 2: the mix has to reconcile against the request count", got.Total())
	}
}

// The three policies with no index still have a mix, and it is theirs rather
// than a column of zeros under policy 4's reasons.
func TestTheBaselinePoliciesHaveTheirOwnMix(t *testing.T) {
	start := time.Unix(1757000000, 0)
	got := bench.Summarize([]bench.Result{
		decided(start, policy.ReasonRoundRobin),
		decided(start, policy.ReasonLeastOutstanding),
		decided(start, policy.ReasonSessionAffinity),
		decided(start, policy.ReasonSessionUnidentified),
	}, bench.SummaryOptions{SLO: slo}).Decisions

	if got.RoundRobin != 1 || got.LeastOutstanding != 1 || got.SessionAffinity != 1 || got.SessionUnidentified != 1 {
		t.Errorf("the baseline reasons counted as %+v", got)
	}
}

// The mix has to survive the round trip to Parquet, because Parquet is what the
// analysis reads. It is the one column group here that is nested rather than
// flat, so it is the one that could silently fail to compact.
func TestTheDecisionMixSurvivesCompaction(t *testing.T) {
	dir := t.TempDir()
	cells, _ := sweepUnderTest(t, dir, bench.SweepConfig{Concurrencies: []int{1}})

	compacted, err := bench.Compact(dir)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	read, err := parquet.ReadFile[bench.Cell](compacted.CellsPath)
	if err != nil {
		t.Fatalf("read %s: %v", compacted.CellsPath, err)
	}
	if len(read) != len(cells) {
		t.Fatalf("read %d cells, want %d", len(read), len(cells))
	}
	if read[0].Decisions.Total() != cells[0].Decisions.Total() {
		t.Errorf("the mix lost rows in compaction: %+v then %+v", cells[0].Decisions, read[0].Decisions)
	}
	if read[0].Decisions != cells[0].Decisions {
		t.Errorf("the mix changed in compaction: %+v then %+v", cells[0].Decisions, read[0].Decisions)
	}
	if cells[0].Decisions.Total() == 0 {
		t.Error("the cell under test recorded no decisions, so this proves nothing about compaction")
	}
}
