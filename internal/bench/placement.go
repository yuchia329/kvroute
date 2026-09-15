package bench

import (
	"fmt"
	"maps"
	"slices"
)

// Placement is how a cell's measured requests were spread across the replicas
// that served them: the imbalance half of idea.md §5, counted.
//
// It exists because the column that was supposed to answer this could not. Every
// row carries the chosen replica's inflight at the moment the decision was made,
// and under session affinity that column read zero on every row ever recorded
// before 9311e68 — the ring stored candidates rather than replicas, and a ring is
// rebuilt only when the replica set changes, so the load it reported was frozen
// at whichever request first built it, on an idle fleet (#27). Nothing routed
// differently, because the hash weighs no load by design. What was lost is the
// evidence: how badly the policy leaves the fleet imbalanced had been resting on
// a column of zeros.
//
// Counting is not merely the repair, it is the stronger measurement. The
// inflight column is the router's own bookkeeping, sampled once per decision,
// and it is only ever as true as the policy that filled it in — which is exactly
// what failed, silently, for the life of the policy. A count of rows asks the
// router for nothing but the replica it named, and no staleness anywhere in its
// belief can corrupt it.
//
// Derived scalars rather than the per-replica map they are reduced from, because
// these compact to Parquet columns and are read by a query, and replica ids are
// not a closed set the way decision reasons are. A map would put the fleet's
// topology into the schema, so a cell that ran on five replicas and one that ran
// on six would not share one. Nothing is lost by reducing: the rows these are
// counted over are themselves the system of record, so any other statistic over
// the same placements is recomputable from them.
type Placement struct {
	// Counted says these figures were taken at all. False on every cell recorded
	// before #27, which is the truth about them: nobody counted their
	// placements. Without it a cell that predates the count and a cell whose
	// every request the router refused are the same empty record, and a whole
	// sweep of the former would read as a fleet that served nothing.
	Counted bool `json:"counted" parquet:"counted"`
	// Requests is the measured requests that named a replica, whatever became of
	// them afterwards.
	//
	// Whatever became of them is deliberate. A request a replica accepted and
	// then errored on still occupied it, and counting only successes would let a
	// policy look balanced by driving one card until it fell over — the same
	// trap the outcome taxonomy keeps dropped, failed and SLO violations in
	// separate columns to avoid.
	Requests int `json:"requests" parquet:"requests"`
	// Unplaced is measured requests that named no replica, which is a request
	// the router never placed anywhere. Counted apart rather than dropped, so
	// the two add back to the cell's own request count instead of quietly
	// disagreeing with it — the reason DecisionMix carries Undecided.
	Unplaced int `json:"unplaced" parquet:"unplaced"`
	// Replicas is how many replicas served at least one of those requests, and
	// so the denominator FairShare is one over.
	//
	// It counts replicas that appeared in the rows, not the fleet. A replica
	// that served nothing at all is in no row and cannot be counted here, so on
	// a fleet where one card went entirely unused both figures below understate
	// the imbalance rather than overstate it. Publishing the count beside them
	// is what makes that legible: a row whose replicas are fewer than the fleet
	// has is a row to read twice.
	Replicas int `json:"replicas" parquet:"replicas"`

	// The two ends of the distribution, each named as well as counted. Named
	// because which replica it was is the first thing anyone asks of an
	// imbalance figure, and on this fleet it is often the answer — GPU 3
	// throttles under full load (#25), and a busiest replica that is always the
	// same card is a hardware result rather than a routing one.
	Busiest          string `json:"busiest" parquet:"busiest"`
	BusiestRequests  int    `json:"busiest_requests" parquet:"busiest_requests"`
	Quietest         string `json:"quietest" parquet:"quietest"`
	QuietestRequests int    `json:"quietest_requests" parquet:"quietest_requests"`
}

// BusiestShare is the share of placed requests the busiest replica took, and
// whether there were any to share out.
//
// It is the figure to read across cells. The spread is a ratio between two
// counts and swings on a quiet replica's handful of requests; this one has every
// placed request in its denominator, so it is comparable between a cell that
// served eighty requests and one that served eight thousand.
func (p Placement) BusiestShare() (float64, bool) {
	if !p.Counted || p.Requests == 0 {
		return 0, false
	}
	return float64(p.BusiestRequests) / float64(p.Requests), true
}

// FairShare is what one replica would have carried had the load been even: the
// line BusiestShare means nothing without.
//
// A busiest share of 22% is near perfect on five replicas and a rout on twenty,
// and a table that published the share without it would be asking its reader to
// know the fleet size from somewhere else.
func (p Placement) FairShare() (float64, bool) {
	if !p.Counted || p.Replicas == 0 {
		return 0, false
	}
	return 1 / float64(p.Replicas), true
}

// Spread is the busiest replica's requests over the quietest one's, and whether
// there were two replicas to have a spread between.
//
// One replica reports none rather than 1.0. A ratio between a replica and itself
// is not a measure of how evenly anything was spread, and a cell that pinned
// every request to one card would otherwise publish the most balanced figure in
// the table.
func (p Placement) Spread() (float64, bool) {
	if !p.Counted || p.Replicas < 2 || p.QuietestRequests == 0 {
		return 0, false
	}
	return float64(p.BusiestRequests) / float64(p.QuietestRequests), true
}

// String is the placement as a table cell: the busiest replica's share against
// the fair share it is read against, and the spread behind it.
//
// An em dash is a cell nobody counted, which must not print as a balanced one.
func (p Placement) String() string {
	share, ok := p.BusiestShare()
	if !ok {
		return "—"
	}
	fair, _ := p.FairShare()
	s := fmt.Sprintf("%.0f%% of %d (fair %.0f%%)", share*100, p.Replicas, fair*100)
	if spread, ok := p.Spread(); ok {
		s += fmt.Sprintf(", %.2f×", spread)
	}
	return s
}

// placementCounter accumulates a cell's placements as its rows are read, so that
// counting them costs the summary no second pass over the rows.
type placementCounter struct {
	perReplica map[string]int
	unplaced   int
}

// count adds one measured request to the replica that served it.
func (c *placementCounter) count(replica string) {
	if replica == "" {
		c.unplaced++
		return
	}
	if c.perReplica == nil {
		c.perReplica = map[string]int{}
	}
	c.perReplica[replica]++
}

// placement reduces the counts to the scalars the record carries.
//
// Replicas are walked in id order, and the comparisons are strict, so a tie for
// busiest or quietest goes to the lowest id. Without that the answer would come
// out of a map's iteration order: two readings of identical rows could name
// different replicas, and a figure that is not a function of the rows it is
// counted from is not reproducible from them. The hash ring breaks its own ties
// by id for the same reason.
func (c *placementCounter) placement() Placement {
	p := Placement{Counted: true, Unplaced: c.unplaced, Replicas: len(c.perReplica)}
	for _, id := range slices.Sorted(maps.Keys(c.perReplica)) {
		n := c.perReplica[id]
		p.Requests += n
		if p.Busiest == "" || n > p.BusiestRequests {
			p.Busiest, p.BusiestRequests = id, n
		}
		if p.Quietest == "" || n < p.QuietestRequests {
			p.Quietest, p.QuietestRequests = id, n
		}
	}
	return p
}
