package bench

import (
	"fmt"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// DecisionMix is how a cell's requests were routed, counted by the reason the
// policy gave.
//
// It is a reported result rather than a diagnostic. Goodput says what a policy
// achieved and this says what it did, and the two are only meaningful together:
// two spill thresholds can produce the same goodput by routing entirely
// differently, and choosing between them without the counts is guesswork. It is
// also the only thing that can tell a policy which is not working from one that
// is working and not helping — a prefix-affinity cell whose decisions are all
// cold has an index that found nothing, and no goodput figure beside it would
// say so.
//
// One field per reason rather than a map, because the reasons are a closed set
// and these columns are compacted to Parquet and read by a query. A map would
// put the schema in the data, and a reason nobody happened to emit would be
// absent from a cell rather than zero in it.
type DecisionMix struct {
	RoundRobin          int `json:"round_robin" parquet:"round_robin"`
	LeastOutstanding    int `json:"least_outstanding" parquet:"least_outstanding"`
	SessionAffinity     int `json:"session_affinity" parquet:"session_affinity"`
	SessionUnidentified int `json:"session_unidentified" parquet:"session_unidentified"`
	PrefixAffinity      int `json:"prefix_affinity" parquet:"prefix_affinity"`
	Cold                int `json:"cold" parquet:"cold"`
	// SpillKV and SpillLoad are the two spill conditions, and they stay two
	// columns all the way to the table. Collapsing them here would undo in the
	// summary what the policy took two reasons to keep apart: the pressure grid
	// crosses working set ratio with skew precisely because memory pressure and
	// load imbalance are physically different and fire these two branches
	// separately.
	SpillKV   int `json:"spill_kv" parquet:"spill_kv"`
	SpillLoad int `json:"spill_load" parquet:"spill_load"`
	// PromptUntokenized is a request exact residency could not look up, because
	// the engine did not tokenize its prompt in time. Its own column rather than
	// cold, which says the index found nothing: a cell full of these had an index
	// nobody could ask.
	PromptUntokenized int `json:"prompt_untokenized" parquet:"prompt_untokenized"`
	// Undecided is a request that carries no decision, which is one the router
	// never placed. Counted rather than dropped, so the mix reconciles against
	// the cell's request count instead of quietly disagreeing with it.
	Undecided int `json:"undecided" parquet:"undecided"`
}

// count adds one request's decision to the mix.
func (m *DecisionMix) count(reason string) {
	switch policy.Reason(reason) {
	case policy.ReasonRoundRobin:
		m.RoundRobin++
	case policy.ReasonLeastOutstanding:
		m.LeastOutstanding++
	case policy.ReasonSessionAffinity:
		m.SessionAffinity++
	case policy.ReasonSessionUnidentified:
		m.SessionUnidentified++
	case policy.ReasonPrefixAffinity:
		m.PrefixAffinity++
	case policy.ReasonCold:
		m.Cold++
	case policy.ReasonSpillKV:
		m.SpillKV++
	case policy.ReasonSpillLoad:
		m.SpillLoad++
	case policy.ReasonPromptUntokenized:
		m.PromptUntokenized++
	default:
		// An unrecognised reason lands here with the requests that carried none.
		// A router emitting a reason this harness does not know is a version
		// skew between the two halves of one run, and burying it in a column of
		// its own would hide it; the total still reconciles, which is what makes
		// it findable.
		m.Undecided++
	}
}

// Spilled is how many requests had a real prefix match declined, under either
// condition. Derived rather than stored, so it cannot disagree with the two
// counts it is the sum of.
func (m DecisionMix) Spilled() int { return m.SpillKV + m.SpillLoad }

// Total is every request in the mix, which is every measured request of the
// cell.
func (m DecisionMix) Total() int {
	return m.RoundRobin + m.LeastOutstanding + m.SessionAffinity + m.SessionUnidentified +
		m.PrefixAffinity + m.Cold + m.SpillKV + m.SpillLoad + m.PromptUntokenized + m.Undecided
}

// AffinityRate is the share of decisions that took a prefix match. Zero when
// nothing was decided, rather than a division by nothing.
func (m DecisionMix) AffinityRate() float64 {
	if total := m.Total(); total > 0 {
		return float64(m.PrefixAffinity) / float64(total)
	}
	return 0
}

// SpillRate is the share of decisions that declined one.
func (m DecisionMix) SpillRate() float64 {
	if total := m.Total(); total > 0 {
		return float64(m.Spilled()) / float64(total)
	}
	return 0
}

// String renders only the reasons that actually fired, so a policy's mix reads
// as its own decisions rather than as six zeros belonging to other policies.
func (m DecisionMix) String() string {
	parts := make([]string, 0, 9)
	for _, named := range []struct {
		reason policy.Reason
		count  int
	}{
		{policy.ReasonRoundRobin, m.RoundRobin},
		{policy.ReasonLeastOutstanding, m.LeastOutstanding},
		{policy.ReasonSessionAffinity, m.SessionAffinity},
		{policy.ReasonSessionUnidentified, m.SessionUnidentified},
		{policy.ReasonPrefixAffinity, m.PrefixAffinity},
		{policy.ReasonCold, m.Cold},
		{policy.ReasonSpillKV, m.SpillKV},
		{policy.ReasonSpillLoad, m.SpillLoad},
		{policy.ReasonPromptUntokenized, m.PromptUntokenized},
	} {
		if named.count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", named.reason, named.count))
		}
	}
	if m.Undecided > 0 {
		parts = append(parts, fmt.Sprintf("undecided=%d", m.Undecided))
	}
	if len(parts) == 0 {
		return "no decisions"
	}
	return strings.Join(parts, " ")
}
