package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/router"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// ConcurrencySweep is the scaling axis: how each policy's latency degrades as
// offered load rises.
var ConcurrencySweep = []int{1, 4, 8, 16, 32, 64, 128, 256}

// ArrivalRateSweep is the ladder the headline goodput number is read off, in
// requests per second offered to the whole fleet. It is the open-loop axis, as
// ConcurrencySweep is the closed-loop one.
//
// It brackets the knee rather than climbing to an arbitrary ceiling: goodput
// rises with offered load until the SLO starts failing and then falls, so the
// figure only means anything if the ladder has points on both sides of the
// turn. It is a ladder for a measured fleet rather than a constant, and it has
// been re-cut once already on exactly the terms the first version set for
// itself: a run that turns before its second rung should move it rather than
// report the edge of the range as the answer.
//
// The first ladder was 4, 8, 12, 16, 24, 32, 48, chosen before the fleet had
// been driven open-loop. The 2026-09-08 two-policy run turned between 12 and 16
// — round-robin held 12.00 of an offered 12 and collapsed to 2.69 at 16 — so
// 24, 32 and 48 all returned exactly 0.00 goodput on every repetition of both
// policies. Three of seven rungs measured nothing, and one rung sat anywhere
// near the turn.
//
// This ladder is re-cut for the multi-turn workload, which is heavier than the
// fixed one that produced those numbers. A fixed request is ~512 prompt tokens;
// a multi-turn request averages ~1,250 across a four-turn session, because each
// turn resends the history before it. That is ~2.4x the prefill for a policy
// that does not keep a conversation together, which should pull round-robin's
// turn down towards 5, while a policy that does keep one together pays for the
// new text only and should turn nearer the old 12 to 16. The rungs span both,
// evenly, so whichever end a policy turns at there are points either side of it.
//
// The top rung is 20 rather than 16 to protect the policy most likely to turn
// late. A conversation kept on one replica finds its history already cached, so
// it computes only its new text — about 448 tokens against the ~1,250 a scattered
// conversation pays, and less than the ~512 of the fixed workload that turned at
// 12 to 16. Session affinity could therefore turn at or above 16, and a ladder
// ending exactly at its peak could not show it turning at all.
//
// Every policy in one comparison has to climb the same ladder. A rung one policy
// skipped is a gap in that row of the table, not a lower score, so this is
// settled before the first sweep rather than tuned between them.
var ArrivalRateSweep = []float64{2, 4, 6, 8, 10, 12, 14, 16, 20}

// FormatLevels renders concurrency levels into the comma-separated spec the
// commands take, so a flag's default can be the package's own value rather than
// a second copy of it that drifts.
func FormatLevels(levels []int) string {
	return formatSpec(levels, strconv.Itoa)
}

// ParseLevels reads a comma-separated spec of concurrency levels.
//
// Here rather than in each command because several commands take the same spec,
// and two parsers for one format is two places for "0" or "-4" to be accepted by
// one and rejected by the other.
func ParseLevels(spec string) ([]int, error) {
	return parseSpec(spec, "load level", "a positive integer", strconv.Atoi)
}

// formatSpec and parseSpec are the comma-separated spec both load axes are
// written in. One implementation, so the two axes cannot come to disagree about
// what an empty field, a stray space or a zero means — which is the same reason
// the parser lives here rather than in each command.
func formatSpec[T any](values []T, format func(T) string) string {
	fields := make([]string, 0, len(values))
	for _, v := range values {
		fields = append(fields, format(v))
	}
	return strings.Join(fields, ",")
}

// parseSpec parses each field with parse and rejects any that is not positive.
// what names the thing being parsed and want describes what a field must be, so
// the error says which axis was wrong and what it wanted.
func parseSpec[T interface{ ~int | ~float64 }](spec, what, want string, parse func(string) (T, error)) ([]T, error) {
	var values []T
	for field := range strings.SplitSeq(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		v, err := parse(field)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("bench: %s %q is not %s", what, field, want)
		}
		values = append(values, v)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("bench: no %ss given", what)
	}
	return values, nil
}

// FormatRates renders arrival rates into the comma-separated spec the commands
// take, for the same reason FormatLevels does.
func FormatRates(rates []float64) string {
	return formatSpec(rates, func(r float64) string { return strconv.FormatFloat(r, 'g', -1, 64) })
}

// ParseRates reads a comma-separated spec of arrival rates in requests per
// second. Fractional rates are allowed: a rate is an offered schedule, not a
// count of anything.
func ParseRates(spec string) ([]float64, error) {
	return parseSpec(spec, "arrival rate", "a positive number of requests per second",
		func(field string) (float64, error) { return strconv.ParseFloat(field, 64) })
}

// Load is one point of a sweep's load axis.
//
// The two drivers differ in exactly one thing — which side of the loop is held
// fixed — so the sweep varies one type rather than carrying two lists and a mode
// flag. A closed-loop point holds a concurrency and offered load is the outcome;
// an open-loop point holds an arrival rate and concurrency is the outcome.
type Load struct {
	Driver      Driver
	Concurrency int
	ArrivalRate float64
}

// ClosedLoopAt is one point of the concurrency axis.
func ClosedLoopAt(concurrency int) Load {
	return Load{Driver: ClosedLoopDriver, Concurrency: concurrency}
}

// OpenLoopAt is one point of the arrival rate axis.
func OpenLoopAt(rate float64) Load {
	return Load{Driver: OpenLoopDriver, ArrivalRate: rate}
}

// Key is the load's part of a cell id: `c8` for eight virtual users, `a12.5` for
// twelve and a half requests per second. Two letters rather than one shared one,
// so a cell's file name says which axis it is on.
func (l Load) Key() string {
	if l.Driver == OpenLoopDriver {
		return "a" + strconv.FormatFloat(l.ArrivalRate, 'g', -1, 64)
	}
	return "c" + strconv.Itoa(l.Concurrency)
}

// String is how a load reads in a table: what was held fixed, in its own units.
func (l Load) String() string {
	if l.Driver == OpenLoopDriver {
		return fmt.Sprintf("%g req/s", l.ArrivalRate)
	}
	return fmt.Sprintf("%d users", l.Concurrency)
}

// validate reports whether this point can be run and recorded.
//
// Both axes are bounded at LoadLevelsPerDriver, and for the same reason:
// overrunning the half of the workload's user space this driver's levels occupy
// puts a cell on another cell's prompts, which makes the second of them read the
// replicas' prefix caches instead of measuring prefill. checkWorkloadPartition
// catches that within one sweep, but two sweeps into one directory — which is a
// supported way to run both axes — are two calls it never sees together. The
// bound holds across them, and it is far above anything six 3090s can serve.
func (l Load) validate() error {
	switch l.Driver {
	case OpenLoopDriver:
		if l.ArrivalRate <= 0 {
			return fmt.Errorf("bench: an open-loop point needs a positive arrival rate, got %g", l.ArrivalRate)
		}
		if l.ArrivalRate >= LoadLevelsPerDriver {
			return fmt.Errorf("bench: an arrival rate of %g is above the %d the workload's user space is partitioned for",
				l.ArrivalRate, LoadLevelsPerDriver)
		}
	case ClosedLoopDriver, "":
		if l.Concurrency <= 0 {
			return fmt.Errorf("bench: a closed-loop point needs a positive concurrency, got %d", l.Concurrency)
		}
		if l.Concurrency >= LoadLevelsPerDriver {
			return fmt.Errorf("bench: a concurrency of %d is above the %d the workload's user space is partitioned for",
				l.Concurrency, LoadLevelsPerDriver)
		}
	default:
		return fmt.Errorf("bench: unknown driver %q", l.Driver)
	}
	return nil
}

// Cell is one benchmark data point: a fixed policy, load level and repetition,
// with its own summary and its own contamination evidence.
type Cell struct {
	ID     string `json:"id" parquet:"id"`
	Policy string `json:"policy" parquet:"policy"`
	// Driver, Concurrency and ArrivalRate are the cell's load axis, flattened:
	// they are the fields of Load, kept flat because the row schema is flat and
	// a cell that nested them would not compact to the same columns.
	Driver      Driver  `json:"driver" parquet:"driver"`
	Concurrency int     `json:"concurrency" parquet:"concurrency"`
	ArrivalRate float64 `json:"arrival_rate" parquet:"arrival_rate"`
	Repetition  int     `json:"repetition" parquet:"repetition"`
	Workload    string  `json:"workload" parquet:"workload"`
	// KVHighWater and LoadImbalanceFactor are the spill grid point this cell
	// ran at, flattened for the reason the load axis is. Both zero under a
	// policy with no spill rule, and under prefix affinity run without one.
	//
	// On the cell rather than only in the sweep's configuration because the
	// tunable sweep is a table whose rows are these two numbers: a directory of
	// cells that did not each carry the point they ran at could not be read as
	// a grid at all, and two grid points' cells would differ in nothing.
	KVHighWater         float64 `json:"kv_high_water" parquet:"kv_high_water"`
	LoadImbalanceFactor float64 `json:"load_imbalance_factor" parquet:"load_imbalance_factor"`
	// HashLeadingBlocks and HashWeight are the stateless prefix hash's grid
	// point: how many leading prefix blocks it hashed, and what that hash was
	// worth against load. Both zero under every other policy, which hashes
	// nothing.
	//
	// On the cell for the reason the spill point is, and more so: the weighting
	// between the two terms is the whole of that policy, so a directory of cells
	// that did not each carry the point they ran at would be a table of one
	// policy measured at settings nothing records.
	HashLeadingBlocks int     `json:"hash_leading_blocks" parquet:"hash_leading_blocks"`
	HashWeight        float64 `json:"hash_weight" parquet:"hash_weight"`
	// ArrivalPlan is how an open-loop cell mapped its arrivals onto
	// conversations; empty for a closed-loop cell, which has no pool to rotate
	// through. See bench.ArrivalPlan for why the workload name cannot carry it.
	ArrivalPlan string `json:"arrival_plan" parquet:"arrival_plan"`
	// ThinkTimeNs is how long a session waited between its turns, which under
	// the open-loop driver is the gap between one conversation's consecutive
	// turns and, by Little's law with the arrival rate, the number of
	// conversations the cell held open at once. Zero for a closed-loop cell,
	// which sends a session's next turn when its last response lands and so has
	// no think time to record.
	//
	// On the cell for the reason the spill point is. It is not part of the
	// workload's name — the same trace is offered at any think time, and the
	// bytes do not change — but it decides how stale a belief is when the
	// router acts on it, which is the axis #17's second criterion is plotted
	// against. Two cells at one arrival rate and two think times would
	// otherwise resolve to the same id, carry identical records, and silently
	// resume one another: the second cell would send nothing and the recency
	// curve would be one configuration measured twice.
	ThinkTimeNs int64 `json:"think_time_ns" parquet:"think_time_ns"`
	// KVEvents says the fleet was publishing its KV cache events while this cell
	// ran. It is an engine setting rather than part of the workload — the bytes
	// are the same either way — so, like the think time, it is on the cell and not
	// in the workload's name. Exact residency cannot run without it, and
	// publishing is work the engine does on every scheduler step, so a sweep
	// refuses to resume cells recorded under the other setting and compare refuses
	// to put the two in one table (ADR-0010). False on every cell recorded before
	// #24, which is the truth about them: no fleet published before it.
	KVEvents bool `json:"kv_events" parquet:"kv_events"`
	// CellDurationNs and WarmupNs are how long this cell kept starting new
	// turns and how much of that opening stretch was recorded but left out of
	// the summary. Its measured window is the difference.
	//
	// On the cell for the reason the think time is: neither changes a byte of
	// what is sent, so neither is in the workload's name, and two cells of one
	// id that differ only here would otherwise resume one another. #18's smoke
	// cell would have done exactly that — 150 s, sharing an id with the first
	// repetition of a grid that then ran at 300 s. A zero length means the cell
	// predates these fields; a zero warm-up is a real setting, so it is the
	// length that says whether the pair was recorded.
	CellDurationNs int64 `json:"cell_duration_ns" parquet:"cell_duration_ns"`
	WarmupNs       int64 `json:"warmup_ns" parquet:"warmup_ns"`

	StartedAtNs int64 `json:"started_at_ns" parquet:"started_at_ns"`
	EndedAtNs   int64 `json:"ended_at_ns" parquet:"ended_at_ns"`

	// The prefix-cache counters the fleet moved over this cell's measured
	// window: vLLM's own reported figure, and the ground truth a cache-aware
	// policy's claim is judged against. Flat for the same reason the load axis
	// is — the row schema is flat, and a nested reading would not compact to the
	// same columns — and reassembled by PrefixCache.
	//
	// PrefixCacheRead is kept beside the counts so "no hits" and "nobody looked"
	// do not read the same. A cell whose replicas could not be scraped has no
	// evidence, and a zero in this column would be a scrape failure published as
	// a measurement.
	PrefixCacheHits    float64 `json:"prefix_cache_hits" parquet:"prefix_cache_hits"`
	PrefixCacheQueries float64 `json:"prefix_cache_queries" parquet:"prefix_cache_queries"`
	PrefixCacheRead    bool    `json:"prefix_cache_read" parquet:"prefix_cache_read"`

	// The prompt-token counters the fleet moved over the same window, flat for
	// the same reason and reassembled by Prefill.
	//
	// They are the physical measurement beside the cache's ratio: prompt tokens
	// the GPUs actually had to compute, which is the work cache-aware routing
	// exists to remove. Two policies sending identical bytes and computing
	// different numbers of prompt tokens differ by redundant prefill, and no hit
	// rate on its own can say by how much.
	PromptTokens       float64 `json:"prompt_tokens" parquet:"prompt_tokens"`
	PromptTokensCached float64 `json:"prompt_tokens_cached" parquet:"prompt_tokens_cached"`
	PrefillRead        bool    `json:"prefill_read" parquet:"prefill_read"`

	// WorkingSet is the WS point this cell's workload offered: session tokens
	// over the measured aggregate fleet KV. Zero when the workload states none —
	// the fixed workload has no session pool, and a multi-turn pool given as a
	// count with no measured capacity has no denominator — which is an absence
	// rather than a point on the axis.
	//
	// On the cell rather than only inside the workload name, because #17 plots
	// belief divergence against it and a report that had to parse a name to find
	// its own axis would break the first time the name gained a field.
	WorkingSet float64 `json:"working_set" parquet:"working_set"`
	// Skew is the Zipf exponent this cell's workload concentrated its draws by.
	//
	// Beside the working set rather than only in the workload name, and for a
	// sharper reason than convenience: skew discounts how much of the session
	// pool a cell actually touches, so a working set binned without it pools
	// cells whose realised pressure differs several-fold and flattens the very
	// curve the pressure grid is drawn to show.
	Skew float64 `json:"skew" parquet:"skew"`

	// PrefixIndexNodes and PrefixIndexCap are how much belief the router's index
	// was holding when this cell ended, and what it was allowed to hold. Both
	// zero under the three policies that consult no index.
	//
	// They are recorded because the node cap is a modelling decision (ADR-0006)
	// and this is the evidence for whether it ever bound: an index that never
	// filled its cap did not over-predict because of the cap, and calibrating the
	// cap against divergence without that distinction would be treating the TTL's
	// failure as the cap's. Cumulative across the sweep rather than per cell,
	// because the router is not restarted between cells — which is what the
	// question needs, since it asks whether the cap ever bound at all.
	PrefixIndexNodes int `json:"prefix_index_nodes" parquet:"prefix_index_nodes"`
	PrefixIndexCap   int `json:"prefix_index_cap" parquet:"prefix_index_cap"`

	// Ejections is how many times the router ejected a replica while this cell
	// ran, read off its stats either side of the cell. A cell during which it
	// moved measured a smaller fleet for part of its window, and because the
	// router reroutes around a missing replica rather than dropping into the gap,
	// nothing else in the cell would show it. Such a cell is flagged (ADR-0009).
	Ejections int `json:"ejections" parquet:"ejections"`
	// EjectionsRead says whether the router's stats were read either side of the
	// cell, so an ejection count nobody could take does not read as a fleet that
	// stayed whole. Recorded rather than flagged, as PrefixCacheRead is: cells
	// recorded before the router published ejections have none to read.
	EjectionsRead bool `json:"ejections_read" parquet:"ejections_read"`
	// OutOfRotation names the replicas the router was not routing to when the
	// cell ended. A cell that ended with one measured a smaller fleet for at
	// least its tail and is flagged, and the sweep stops rather than run the next
	// cell on a fleet it does not describe.
	OutOfRotation []string `json:"out_of_rotation,omitempty" parquet:"out_of_rotation"`

	// ResidencyLost, ResidencyResets and ResidencyReconnects are what exact
	// residency's event streams went through over this cell, summed across
	// replicas and read off the router's stats either side of the cell: batches
	// of history no replay could recover, the resets they forced, and the
	// reconnects that each started a replica from nothing. ResidencyOrphaned is
	// the stored runs the index could not place over the same window. All zero
	// under every other policy, which follows no stream.
	//
	// A cell any of the first three moved in ran on an index that knew less than
	// it claimed for part of its window, and is flagged (ADR-0010). Orphans are
	// recorded and not flagged: a duplicate block evicted while its twin lives on
	// orphans the next run too, which is the index forgetting what it cannot
	// vouch for rather than history lost.
	ResidencyLost       uint64 `json:"residency_lost" parquet:"residency_lost"`
	ResidencyResets     uint64 `json:"residency_resets" parquet:"residency_resets"`
	ResidencyReconnects uint64 `json:"residency_reconnects" parquet:"residency_reconnects"`
	ResidencyOrphaned   uint64 `json:"residency_orphaned" parquet:"residency_orphaned"`
	// ResidencyRead says the router reported residency on both sides of the
	// cell, so zeros above are streams that lost nothing rather than nobody
	// having looked.
	ResidencyRead bool `json:"residency_read" parquet:"residency_read"`
	// ResidencyDisconnected names the replicas whose event stream was not
	// connected when the cell ended, leaving the index following nothing for
	// them.
	ResidencyDisconnected []string `json:"residency_disconnected,omitempty" parquet:"residency_disconnected"`

	Summary       `json:"summary"`
	Contamination `json:"contamination"`
}

// PrefixCache is what the fleet's prefix caches served over this cell,
// reassembled from the flat columns it is recorded in.
func (c Cell) PrefixCache() vllmmetrics.PrefixCache {
	return vllmmetrics.PrefixCache{Hits: c.PrefixCacheHits, Queries: c.PrefixCacheQueries, Read: c.PrefixCacheRead}
}

// Prefill is what the fleet's GPUs had to compute over this cell, reassembled
// from the flat columns it is recorded in.
func (c Cell) Prefill() vllmmetrics.Prefill {
	return vllmmetrics.Prefill{PromptTokens: c.PromptTokens, CachedTokens: c.PromptTokensCached, Read: c.PrefillRead}
}

// BytesPerToken is the prompt bytes-per-token ratio this cell measured: the
// bytes it offered over the tokens the engines said they processed.
//
// Per cell rather than as a constant, because it is a property of the workload's
// prompts and the model's tokenizer together, and a run that changed either
// would carry a ratio that no longer converts its own figures.
func (c Cell) PromptBytesPerToken() (float64, bool) {
	return prefix.MeasurePromptBytesPerToken(c.PromptBytes, c.Prefill())
}

// arrivalPlanFor is the plan a cell of this load axis ran under. Only the
// open-loop driver rotates arrivals through a conversation pool; a closed-loop
// cell's virtual users walk the workload directly and have no plan to record.
func arrivalPlanFor(load Load) string {
	if load.Driver == OpenLoopDriver {
		return ArrivalPlan
	}
	return ""
}

// thinkTimeFor is the think time a cell actually ran at, which is not always the
// one it was configured with: zero means the driver's default, and a closed-loop
// cell has none at all.
//
// Resolved here rather than recorded raw so that a cell configured with zero and
// one configured with five seconds do not look different on the record while
// having offered the same load. The guard in checkCachedWorkload compares
// resolved values for the same reason.
func thinkTimeFor(load Load, configured time.Duration) time.Duration {
	if load.Driver != OpenLoopDriver {
		return 0
	}
	if configured <= 0 {
		return DefaultThinkTime
	}
	return configured
}

// Load is the point of the load axis this cell sits on, reassembled from the
// flat columns it is recorded in.
func (c Cell) Load() Load {
	return Load{Driver: c.Driver, Concurrency: c.Concurrency, ArrivalRate: c.ArrivalRate}
}

// LoadLevelsPerDriver is how many slices of the workload's user space each
// driver's load levels get inside a repetition's block. See loadOffset.
const LoadLevelsPerDriver = 512

// CellWorkloadOffset is the slice of the workload's user space a cell sends
// from. It is derived from the axes alone — deliberately not from the policy —
// so a re-run of a cell sends its own bytes again and no cell ever re-sends
// another's.
func CellWorkloadOffset(load Load, repetition int) int {
	return (repetition*2*LoadLevelsPerDriver + loadOffset(load)) * WorkloadStride
}

// checkWorkloadPartition refuses a sweep in which two cells would send the same
// prompts.
//
// ADR-0004: a cell's slice of the workload's user space is derived from its axes,
// so two cells sharing a slice send the same bytes and the second of them reads
// the replicas' prefix caches instead of measuring prefill — a seven-fold TTFT
// difference that nothing in the latency admits to. The arithmetic that derives
// the slice cannot separate every pair of load levels a caller might ask for
// (two arrival rates a fraction apart round to one index), so rather than
// trusting it, the whole partition is checked before a single cell runs.
func checkWorkloadPartition(policy string, loads []Load, repetitions int) error {
	seen := map[int]string{}
	for _, load := range loads {
		for repetition := 1; repetition <= repetitions; repetition++ {
			id := CellID(policy, load, repetition)
			offset := CellWorkloadOffset(load, repetition)
			if other, clash := seen[offset]; clash {
				return fmt.Errorf("bench: cells %s and %s would send the same prompts, so the second would measure the replicas' prefix caches rather than prefill: separate their load levels", other, id)
			}
			seen[offset] = id
		}
	}
	return nil
}

// loadOffset is a load level's position within its repetition's block of
// 2 x LoadLevelsPerDriver slices. Concurrency levels take the low half and
// arrival rates the high half, so a concurrency of 8 and a rate of 8 land in
// different slices and never send each other's bytes. The concurrency arithmetic
// is unchanged, so cells recorded before the open-loop driver existed still
// resolve to the slice they were run on.
func loadOffset(load Load) int {
	if load.Driver == OpenLoopDriver {
		return LoadLevelsPerDriver + int(math.Round(load.ArrivalRate))
	}
	return load.Concurrency
}

// CellID is the cell's identity and its cache key. It is derived from the axes
// alone, so re-running a sweep with the same axes finds the same cells.
func CellID(policy string, load Load, repetition int) string {
	return fmt.Sprintf("%s-%s-r%d", policy, load.Key(), repetition)
}

// SweepConfig configures a sweep: a set of cells varying one load axis with
// everything else held fixed.
type SweepConfig struct {
	// Dir holds the sweep's cells. An interrupted sweep resumes from what is
	// already in it.
	Dir string
	// Target is the router's base URL.
	Target string
	// Policy names the policy the router is running. The sweep does not set it
	// — the router is started with it — so this is a label that has to be told
	// the truth.
	Policy string

	// Concurrencies are the closed-loop axis: virtual users held for a whole
	// cell, with offered load as the outcome. Defaults to ConcurrencySweep when
	// no load levels are given at all.
	Concurrencies []int
	// ArrivalRates are the open-loop axis: requests per second offered whether
	// or not earlier requests have finished. This is the axis the headline
	// goodput number comes from, because a closed-loop driver throttles itself
	// at exactly the saturation the number is about.
	//
	// Both may be set, and then one sweep runs both axes: every cell records
	// which driver produced it, so the two never merge into an unreadable
	// column.
	ArrivalRates []float64
	Repetitions  int
	CellDuration time.Duration
	// Warmup is the slice at the start of each cell whose rows are recorded but
	// excluded from the summary.
	Warmup time.Duration
	// FleetWarmup is how many requests to send to each replica directly,
	// before the first cell, to force its first real forward pass. Zero skips
	// it.
	FleetWarmup int
	// Settle is how long to wait between cells, so one cell's tail does not
	// land inside the next one's window.
	Settle time.Duration
	// ThinkTime is how long a session waits between its turns under the
	// open-loop driver, which with the arrival rate sets how many conversations
	// a cell holds open. Zero uses DefaultThinkTime. Closed-loop cells ignore
	// it: there a session's next turn goes out when its last response arrives.
	ThinkTime time.Duration

	// Replicas are the base URLs of every replica the router fronts. Each is
	// asked for /health before the first cell runs. Empty skips the check.
	Replicas []string

	SLO              SLO
	FailureThreshold float64
	// WarmupDriftThreshold flags a cell whose measured window was still
	// speeding up. Zero uses DefaultWarmupDriftThreshold.
	WarmupDriftThreshold float64
	// ScheduleLagThreshold flags an open-loop cell whose driver fell behind the
	// arrival schedule it claims to have offered. Zero uses
	// DefaultScheduleLagThreshold. It says nothing about a closed-loop cell.
	ScheduleLagThreshold time.Duration
	Workload             Workload
	// Spill is the grid point the router is running, and the one these cells
	// will be labelled with. Its zero value is a router with no spill rule,
	// which is every policy but prefix affinity and prefix affinity before the
	// grid is swept.
	//
	// The sweep is told it rather than setting it: the router is a separate
	// process started with its own flags, exactly as it is for the policy. That
	// is why checkRouter verifies it against what the router reports, and why
	// this cannot be left to be inferred.
	Spill policy.Spill
	// HashPoint is the stateless prefix hash's grid point, told to the sweep for
	// the reason Spill is: the router is a separate process started with its own
	// flags, and checkRouter verifies this against what it reports. Its zero
	// value is a router that hashes nothing, which is every policy but that one.
	HashPoint     policy.HashPoint
	Contamination ContaminationConfig
	// FleetKVEvents is whether the fleet is publishing its KV cache events, and
	// what every cell is labelled with. Told rather than probed, like the model
	// and the fleet's cards — ops/versions.env is where it is set — and checked in
	// the one direction the router can confirm: a router following the engines'
	// events proves the fleet publishes them.
	FleetKVEvents bool

	Log *slog.Logger

	// gridOffset is the extra slice of the workload's user space this sweep's
	// pressure point sends from, derived from the workload once at the start
	// rather than per cell.
	//
	// Not a field a caller sets. It is read off the workload the caller already
	// passed, because a sweep that could be told a pressure point different from
	// the one its workload offers is a sweep that can be told to overlap
	// another's prompts — and the whole purpose of the term is that no operator
	// has to remember it.
	gridOffset int
}

// RunSweep runs every cell of the sweep and returns them in order.
//
// Cells already on disk are loaded rather than re-run. The sweep is hours long
// on a shared box, so resumability is not a convenience: without it an
// interruption in the eleventh hour costs the ten before it.
func RunSweep(ctx context.Context, cfg SweepConfig) ([]Cell, error) {
	if cfg.Dir == "" {
		return nil, errors.New("bench: a sweep directory is required")
	}
	if cfg.Policy == "" {
		return nil, errors.New("bench: the policy the router is running must be named")
	}
	loads, err := cfg.loads()
	if err != nil {
		return nil, err
	}
	if cfg.Repetitions <= 0 {
		cfg.Repetitions = 1
	}
	if err := checkWorkloadPartition(cfg.Policy, loads, cfg.Repetitions); err != nil {
		return nil, err
	}
	// The pressure point's slice of the user space, settled before any cell
	// runs. It is refused here rather than rounded, because two grid points
	// sharing a slice is invisible in every figure it corrupts.
	if cfg.Workload != nil {
		// The CONFIGURED point, not the derived one. OfferedWorkingSet derives a
		// ratio from any measured capacity, and keying the partition on that
		// would mean passing -kv-capacity changed the bytes a cell sends —
		// which ADR-0008 promises it does not, and which would split the frozen
		// comparison's table between cells run with the flag and without it.
		if cfg.gridOffset, err = GridWorkloadOffset(GridPoint{
			WorkingSet: ConfiguredWorkingSet(cfg.Workload),
			Skew:       OfferedSkew(cfg.Workload),
		}); err != nil {
			return nil, err
		}
	}
	if err := checkCachedWorkload(cfg); err != nil {
		return nil, err
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.Workload == nil {
		cfg.Workload = NewFixedWorkload(FixedWorkload{})
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	cfg.Contamination.Log = cfg.Log

	if err := checkRouter(ctx, cfg); err != nil {
		return nil, err
	}
	if err := checkFleet(ctx, cfg); err != nil {
		return nil, err
	}
	if err := WarmReplicas(ctx, WarmConfig{
		Replicas: cfg.Replicas,
		Requests: cfg.FleetWarmup,
		Workload: cfg.Workload,
		Log:      cfg.Log,
	}); err != nil {
		return nil, err
	}

	cellDir := filepath.Join(cfg.Dir, "cells")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		return nil, fmt.Errorf("bench: create %s: %w", cellDir, err)
	}

	var cells []Cell
	for _, load := range loads {
		for repetition := 1; repetition <= cfg.Repetitions; repetition++ {
			id := CellID(cfg.Policy, load, repetition)

			cached, ok := loadCell(cellDir, id)
			switch {
			case ok && cached.Contaminated():
				// §6: any cell with a foreign process on any of the six cards is
				// discarded and re-run, never averaged in. Leaving it cached
				// would make "re-run" mean "delete the file by hand first", so
				// the evidence is moved out of the way and the cell recomputed.
				if err := discard(cfg.Dir, cellDir, id); err != nil {
					return cells, err
				}
				cfg.Log.Warn("cached cell was contaminated, discarding and re-running", "cell", id,
					"foreign_procs", cached.ForeignProcs, "probe_errors", cached.ProbeErrors)
			case ok && !cached.matchesSLO(cfg.SLO):
				// The SLO is derived from the measured concurrency-1 floor, so
				// it arrives after the cells it is applied to. SLO violation is
				// a property of the rows, not of the run, so it is recomputed
				// from them rather than costing another hour of GPU time.
				recomputed, err := resummarize(cellDir, cached, cfg)
				if err != nil {
					return cells, err
				}
				cfg.Log.Info("cell is cached, resummarised against the current SLO", "cell", id,
					"goodput_rps", recomputed.GoodputRPS, "slo_violations", recomputed.SLOViolations)
				cells = append(cells, recomputed)
				continue
			case ok:
				cfg.Log.Info("cell is cached, not re-running", "cell", id,
					"goodput_rps", cached.GoodputRPS, "flagged", cached.Flagged)
				cells = append(cells, cached)
				continue
			}
			if err := ctx.Err(); err != nil {
				return cells, err
			}
			if len(cells) > 0 && cfg.Settle > 0 {
				time.Sleep(cfg.Settle)
			}

			cell, err := runCell(ctx, cfg, cellDir, id, load, repetition)
			if err != nil {
				return cells, err
			}
			cells = append(cells, cell)
		}
	}
	return cells, nil
}

func (cfg SweepConfig) summaryOptions() SummaryOptions {
	return SummaryOptions{
		SLO:                  cfg.SLO,
		FailureThreshold:     cfg.FailureThreshold,
		WarmupDriftThreshold: cfg.WarmupDriftThreshold,
		ScheduleLagThreshold: cfg.ScheduleLagThreshold,
	}
}

// loads is the sweep's load axis: its concurrency levels, then its arrival
// rates. Empty means the concurrency sweep, which is what a sweep given no
// levels at all has always run.
func (cfg SweepConfig) loads() ([]Load, error) {
	if len(cfg.Concurrencies) == 0 && len(cfg.ArrivalRates) == 0 {
		cfg.Concurrencies = ConcurrencySweep
	}
	loads := make([]Load, 0, len(cfg.Concurrencies)+len(cfg.ArrivalRates))
	for _, concurrency := range cfg.Concurrencies {
		loads = append(loads, ClosedLoopAt(concurrency))
	}
	for _, rate := range cfg.ArrivalRates {
		loads = append(loads, OpenLoopAt(rate))
	}
	for _, load := range loads {
		if err := load.validate(); err != nil {
			return nil, err
		}
	}
	return loads, nil
}

// WarmConfig configures the direct warm-up.
type WarmConfig struct {
	// Replicas are the base URLs to warm. Empty skips the warm-up.
	Replicas []string
	// Requests is how many to send to each. Zero skips the warm-up.
	Requests int
	Workload Workload
	Log      *slog.Logger
}

// WarmReplicas sends a few requests to each replica directly, before the first
// measurement, so that every replica has done a real forward pass before any
// measured request reaches it.
//
// Directly, not through the router, because the router decides where a request
// goes and the harness does not get a say. Warming through it at concurrency 1
// under round-robin sends the first five requests to replicas 0..4 and leaves
// replica 5 to serve the first *measured* request cold — the warm-up would
// manufacture the very cold start it exists to prevent, in the cell that
// defines the latency floor.
//
// This is a handful of requests once per run, not per cell: what it covers —
// lazy allocation and first-execution kernel paths that survive /health —
// happens once per process. Per-cell transients are what SweepConfig.Warmup is
// for.
//
// The characterization pass needs it for the same reason and more sharply: the
// concurrency-1 measurement it takes is the latency floor every SLO is derived
// from, so a cold first forward pass landing inside it would put a one-off
// compile into the threshold every later cell is judged against.
func WarmReplicas(ctx context.Context, cfg WarmConfig) error {
	if cfg.Requests <= 0 || len(cfg.Replicas) == 0 {
		return nil
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	errs := make([]error, len(cfg.Replicas))

	var wg sync.WaitGroup
	for i, base := range cfg.Replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range cfg.Requests {
				// Turn index n keeps the bodies distinct, so the replica is not
				// answering the same request from its own prefix cache every
				// time and skipping the work being warmed.
				if err := warmOnce(ctx, client, base, cfg.Workload.Next(i, n)); err != nil {
					errs[i] = fmt.Errorf("%s: %w", base, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("bench: warming the fleet failed, so a replica cannot serve: %w", err)
	}
	cfg.Log.Info("fleet warmed", "replicas", len(cfg.Replicas), "requests_each", cfg.Requests)
	return nil
}

func warmOnce(ctx context.Context, client *http.Client, base string, turn Turn) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(base, "/")+router.ChatCompletionsPath, bytes.NewReader(turn.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drained rather than discarded unread, so the warm-up pays the whole cost
	// of a response the way a measured request will.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// checkCachedWorkload refuses a sweep that would resume into cells which sent
// different bytes from the ones it is about to send.
//
// A cell is cached on its id, and an id names the policy, the load point and the
// repetition — deliberately nothing about what was offered, so that two policies
// at one load point resolve to the same slice of the workload's user space and are
// therefore comparable. The cost of that is this: change the workload and the same
// directory happily mixes the old cells with the new ones, and the resulting
// comparison would be drawn across two different traces.
//
// The workload name is what catches it. It carries every knob that changes the
// bytes for exactly this reason (ADR-0004), and `compare` already refuses cells
// whose names differ — but it refuses at the end, after the GPU time is spent. On
// a shared box that is a wasted night, so the same disagreement is caught here,
// before the first cell.
//
// It refuses rather than discarding. A contaminated cell is the fleet's failure
// and re-running it is the only repair; a workload that changed is somebody's
// decision, and deleting hours of good cells is not this function's to make. The
// repair is a new -dir, and the error says so.
func checkCachedWorkload(cfg SweepConfig) error {
	offered := cfg.Workload.Name()
	paths, err := filepath.Glob(filepath.Join(cfg.Dir, "cells", "*.json"))
	if err != nil {
		return fmt.Errorf("bench: %s: %w", cfg.Dir, err)
	}
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var cached Cell
		if json.Unmarshal(contents, &cached) != nil {
			// Not a cell record, or a truncated one. loadCell treats that as an
			// absent cell and re-runs it, and refusing the whole sweep over it
			// would be a harsher answer to a half-written file than the resume
			// path itself gives.
			continue
		}
		if cached.Workload != "" && cached.Workload != offered {
			return fmt.Errorf("bench: %s already holds cells of a different workload, so resuming here would mix two traces into one directory and the comparison drawn from them would span both.\n"+
				"  cell %s offered: %s\n"+
				"  this sweep offers: %s\n"+
				"Sweep into a new -dir. Two cells that sent different bytes are not two measurements of one thing (ADR-0004), and compare refuses them — but only after the GPU time has been spent, which is why this refuses now",
				cfg.Dir, cached.ID, cached.Workload, offered)
		}
		// The same trap one level quieter. Think time is not in the workload's
		// name because it changes no bytes, so two cells at one arrival rate and
		// two think times agree on every field the checks above compare — and
		// share an id. The second sweep would resume the first one's cells,
		// report them as cached, send nothing, and produce a recency curve that
		// is one think time plotted twice.
		//
		// Zero is unstated rather than instant: cells recorded before this field
		// existed carry no think time, and refusing them would invalidate every
		// open-loop cell already measured over a column nobody wrote.
		if cached.ThinkTimeNs != 0 && cached.ThinkTimeNs != thinkTimeFor(cached.Load(), cfg.ThinkTime).Nanoseconds() {
			return fmt.Errorf("bench: %s already holds cells run at a different think time, and think time is not in the workload's name, so these cells would be resumed as though they were this sweep's own.\n"+
				"  cell %s ran at: %v\n"+
				"  this sweep offers: %v\n"+
				"Sweep into a new -dir. The gap between a session's turns decides how stale the router's belief is when it acts on it, so two think times are two measurements and not two repetitions of one",
				cfg.Dir, cached.ID, time.Duration(cached.ThinkTimeNs), thinkTimeFor(cached.Load(), cfg.ThinkTime))
		}
		// And the engine's configuration, which no byte of the workload shows. A
		// fleet publishing its KV cache events does work on every scheduler step
		// that one not publishing does not (ADR-0010), so a sweep of one must not
		// resume cells recorded on the other: it would report them cached and pool
		// an events-off measurement into an events-on comparison — which is what
		// re-running policy 4 for #24 into #18's directory would do.
		if cached.KVEvents != cfg.FleetKVEvents {
			return fmt.Errorf("bench: %s already holds cells recorded %s the fleet publishing its KV cache events, and this sweep's fleet runs %s them, so resuming here would pool two engine configurations as one (cell %s).\n"+
				"Sweep into a new -dir — make pressure-grid picks one of its own when ops/versions.env turns the events on. Publishing is work the engine does on every step, so cells recorded with and without it are two measurements, not repetitions of one (ADR-0010)",
				cfg.Dir, withOrWithout(cached.KVEvents), withOrWithout(cfg.FleetKVEvents), cached.ID)
		}
		// And the stateless hash's grid point, which is the same trap again and
		// the likeliest of them to be sprung: its weight axis is five points at
		// one workload point, so five sweeps differing in nothing else would
		// otherwise land in one directory, and the four after the first would
		// report themselves cached, send nothing, and draw a weight axis that is
		// one weight plotted five times. A cell recorded under a policy that
		// hashes nothing carries a zero window and never compares against one
		// that does, because the two differ in their policy and so in their id.
		if cached.HashLeadingBlocks != 0 &&
			(cached.HashLeadingBlocks != cfg.HashPoint.LeadingBlocks || cached.HashWeight != cfg.HashPoint.HashWeight) {
			return fmt.Errorf("bench: %s already holds cells run at a different hash grid point, and the point is not in the workload's name, so these cells would be resumed as though they were this sweep's own (cell %s).\n"+
				"  cell ran:          %v\n"+
				"  this sweep offers: %v\n"+
				"Sweep each point into its own -dir. The weighting between the hash and the load term is the whole of that policy, so two weights are two measurements and not two repetitions of one",
				cfg.Dir, cached.ID,
				policy.HashPoint{LeadingBlocks: cached.HashLeadingBlocks, HashWeight: cached.HashWeight}, cfg.HashPoint)
		}
		// And the cell's length and warm-up, which no byte of the workload shows
		// either. A cell's measured window is its length less its warm-up, so a
		// cell of another length or warm-up is another measurement under the same
		// id — #18's 150 s smoke cell shared an id with the first repetition of a
		// 300 s grid and was kept out of it only by being deleted by hand. A cell
		// recorded before these fields carries no length and is not compared; a
		// recorded warm-up of zero is a real setting and is, which is why the
		// length and not the warm-up says whether the pair was recorded.
		if cached.CellDurationNs != 0 &&
			(cached.CellDurationNs != cfg.CellDuration.Nanoseconds() || cached.WarmupNs != cfg.Warmup.Nanoseconds()) {
			return fmt.Errorf("bench: %s already holds cells of a different length or warm-up, and neither is in the workload's name, so these cells would be resumed as though they were this sweep's own (cell %s).\n"+
				"  cell ran:          %v long, %v warm-up\n"+
				"  this sweep offers: %v long, %v warm-up\n"+
				"Sweep into a new -dir. A cell's measured window is its length less its warm-up, so two geometries are two measurements and not two repetitions of one",
				cfg.Dir, cached.ID, time.Duration(cached.CellDurationNs), time.Duration(cached.WarmupNs), cfg.CellDuration, cfg.Warmup)
		}
	}
	return nil
}

// withOrWithout names a KV cache events setting for a sentence.
func withOrWithout(on bool) string {
	if on {
		return "with"
	}
	return "without"
}

// checkRouter refuses to start a sweep against a router that is not there, or that
// is running a policy other than the one the cells will be labelled with.
//
// The label is this harness's weakest link. A router is started with its policy and
// the sweep is only told which one that was — SweepConfig.Policy says as much — so
// nothing else stands between a mistyped -policy and a directory of cells naming a
// policy that never ran. Two policies' cells would then differ in nothing at all,
// and the comparison drawn from them would be fiction made of real numbers: one
// policy measured twice. No later analysis can detect that, which is why it is
// checked before the first cell rather than reported after the last.
//
// A router that is not running is the cheaper failure and the same argument: it
// refuses every connection, the driver books each as a dropped request, and the
// sweep runs to its full length producing nothing else. On a box that is only idle
// until the 20th that is a wasted night.
//
// The router's own /router/stats is the only party that knows, which is why the
// harness asks it rather than inferring anything.
func checkRouter(ctx context.Context, cfg SweepConfig) error {
	if cfg.Target == "" {
		return errors.New("bench: a router to drive is required")
	}
	url := strings.TrimSuffix(cfg.Target, "/") + "/router/stats"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("bench: %w", err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("bench: the router at %s did not answer, so every request of this sweep would be a dropped one: %w", cfg.Target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bench: the router at %s answered %s with status %d, so this is not a kvroute router", cfg.Target, url, resp.StatusCode)
	}

	var stats router.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return fmt.Errorf("bench: %s did not return a router's stats, so this is not a kvroute router: %w", url, err)
	}
	if stats.Policy != cfg.Policy {
		return fmt.Errorf("bench: the router at %s is running %q, but this sweep would label its cells %q. "+
			"The router is started with its policy and the sweep is only told which one, so one of the two is wrong — and cells naming a policy that never ran would make a comparison of one policy against itself that no later analysis could catch",
			cfg.Target, stats.Policy, cfg.Policy)
	}
	if err := checkGridPoint(cfg, stats); err != nil {
		return err
	}
	if err := checkHashPoint(cfg, stats); err != nil {
		return err
	}
	// The one direction the router can confirm: a router following the engines'
	// KV cache events is proof the fleet publishes them, and cells labelled as run
	// without them would record an engine configuration that was not running.
	if len(stats.Residency) > 0 && !cfg.FleetKVEvents {
		return fmt.Errorf("bench: the router at %s is following the engines' KV cache events, so the fleet publishes them, but this sweep would record its cells as run without them. "+
			"Pass -fleet-kv-events=true — or, through make, set KV_EVENTS=\"1\" in ops/versions.env, the file the fleet was started from (ADR-0010)", cfg.Target)
	}
	if len(cfg.Replicas) > 0 && len(stats.Replicas) != len(cfg.Replicas) {
		return fmt.Errorf("bench: the router at %s fronts %d replicas but this sweep was given %d, so the harness and the router are pointed at different fleets",
			cfg.Target, len(stats.Replicas), len(cfg.Replicas))
	}
	for _, r := range stats.Replicas {
		if !r.InRotation() {
			return fmt.Errorf("bench: the router at %s is not routing to %s (%s), so every cell of this sweep would measure a smaller fleet than it says — "+
				"and nothing would be dropped to show it, because the router routes around the gap. Restore a drained replica, or let an ejected one pass its health checks, before sweeping",
				cfg.Target, r.ID, r.Rotation())
		}
	}
	cfg.Log.Info("router is up and running the policy these cells will name",
		"router", cfg.Target, "policy", stats.Policy, "spill", cfg.Spill, "hash", cfg.HashPoint, "replicas", len(stats.Replicas))
	return nil
}

// checkGridPoint refuses a sweep whose cells would be labelled with spill
// thresholds the router is not running.
//
// The same argument as the policy check above, one level finer. The tunable
// sweep's whole output is a table indexed by these two numbers, and they reach
// the router as flags on a separate process; if the label and the flag disagree,
// the grid is one point measured nine times and every number in it is real. A
// router that reports no thresholds at all is running a policy that has none, so
// a sweep naming one there is the same mistake in the other direction.
func checkGridPoint(cfg SweepConfig, stats router.Stats) error {
	running := policy.Spill{}
	if stats.Spill != nil {
		running = *stats.Spill
	}
	if running == cfg.Spill {
		return nil
	}
	if stats.Spill == nil {
		return fmt.Errorf("bench: the router at %s reports no spill thresholds, because %s has none to report, but this sweep would label its cells %v",
			cfg.Target, stats.Policy, cfg.Spill)
	}
	return fmt.Errorf("bench: the router at %s is spilling at %v, but this sweep would label its cells %v. "+
		"The thresholds reach the router as its own flags and the sweep is only told what they were, so one of the two is wrong — and a grid of cells labelled with a point that never ran is one point measured nine times, in numbers that are all real",
		cfg.Target, running, cfg.Spill)
}

// checkHashPoint refuses a sweep whose cells would be labelled with a hash
// window or weighting the router is not running.
//
// checkGridPoint's argument, one policy over, and the stakes are the same: the
// stateless hash's whole output is a table indexed by these two numbers, and a
// weight that reached the router as something other than what the cells claim
// would turn the axis this policy exists to sweep into one point measured five
// times, in numbers that are all real.
func checkHashPoint(cfg SweepConfig, stats router.Stats) error {
	running := policy.HashPoint{}
	if stats.HashPoint != nil {
		running = *stats.HashPoint
	}
	if running == cfg.HashPoint {
		return nil
	}
	if stats.HashPoint == nil {
		return fmt.Errorf("bench: the router at %s reports no hash point, because %s hashes nothing, but this sweep would label its cells %v",
			cfg.Target, stats.Policy, cfg.HashPoint)
	}
	return fmt.Errorf("bench: the router at %s is hashing at %v, but this sweep would label its cells %v. "+
		"The window and the weight reach the router as its own flags and the sweep is only told what they were, so one of the two is wrong — and a weight axis whose cells all ran at one point is that point measured five times",
		cfg.Target, running, cfg.HashPoint)
}

// readRouterStats asks the router what it can say about a cell from its own
// side: how much belief its index is holding, and how many times it has ejected
// a replica.
//
// A failure is not an error, for the same reason a failed metrics scrape is not:
// the cell measured whatever it measured, and losing a completed cell over a
// stats endpoint that did not answer would cost far more than the columns do. It
// reports whether it read anything, so that an ejection count is never derived
// from a reading nobody took. The three policies that consult no index report
// none, which reads as zero and is the honest answer — there was no index to be
// full.
func readRouterStats(ctx context.Context, cfg SweepConfig) (router.Stats, bool) {
	stats, err := fetchRouterStats(ctx, cfg.Target)
	if err != nil {
		cfg.Log.Warn("could not read the router's stats, so this cell records no index occupancy and no ejection count", "err", err)
		return router.Stats{}, false
	}
	return stats, true
}

// totalEjections is how many times the router has ejected any of its replicas.
func totalEjections(stats router.Stats) int {
	total := 0
	for _, r := range stats.Replicas {
		total += r.Ejections
	}
	return total
}

// checkFleet refuses to start a sweep unless every replica answers /health.
//
// The router ejects a dead replica and reroutes around it, which makes a missing
// replica worse to start on rather than better: nothing is dropped, so every cell
// quietly measures a smaller fleet than it claims to. The sweep is hours long, so
// the difference between catching that here and catching it in the results is a
// wasted night on a box that is only idle until the 20th.
func checkFleet(ctx context.Context, cfg SweepConfig) error {
	if len(cfg.Replicas) == 0 {
		cfg.Log.Warn("no replica URLs given, so the fleet was not checked before the sweep")
		return nil
	}
	if err := CheckReplicas(ctx, cfg.Replicas); err != nil {
		return err
	}
	cfg.Log.Info("fleet is up", "replicas", len(cfg.Replicas))
	return nil
}

// CheckReplicas refuses to proceed unless every replica answers /health.
//
// Exported for the same reason WarmReplicas is: the characterization pass has
// to establish that it is measuring the whole fleet before it claims anything
// about whether the fleet is interchangeable.
func CheckReplicas(ctx context.Context, replicas []string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	var down []string
	for _, base := range replicas {
		if err := ping(ctx, client, base); err != nil {
			down = append(down, fmt.Sprintf("%s (%v)", base, err))
		}
	}
	if len(down) > 0 {
		return fmt.Errorf("bench: %d of %d replicas did not answer /health, so this would measure an incomplete fleet: %s",
			len(down), len(replicas), strings.Join(down, "; "))
	}
	return nil
}

func ping(ctx context.Context, client *http.Client, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// runCell runs one cell and writes it to disk.
func runCell(ctx context.Context, cfg SweepConfig, cellDir, id string, load Load, repetition int) (Cell, error) {
	// Read before the cell as well as after it, so the ejections the cell
	// records are its own rather than every one since the router started — and
	// so no cell starts on a fleet with a replica already out of rotation. That
	// is what a replica that died in an earlier cell and was not brought back
	// looks like, and every cell run on it would measure a smaller fleet with no
	// ejection of its own to be flagged for.
	before, beforeRead := readRouterStats(ctx, cfg)
	if out := outOfRotation(before); len(out) > 0 {
		return Cell{}, fmt.Errorf("bench: stopping before cell %s: the router is not routing to %s, so this cell and every one after it would measure a smaller fleet than they say. "+
			"Bring the replica back — restart an ejected one and let it pass its health checks, or restore a drained one — and run the sweep again: it resumes from here",
			id, strings.Join(out, ", "))
	}

	// Rows stream to a partial file and are renamed into place only once the
	// cell has finished. A crash therefore leaves readable partial data under a
	// name that is visibly incomplete, and never leaves a half-run cell looking
	// like a cached one.
	rowPath := filepath.Join(cellDir, id+".jsonl")
	partialPath := rowPath + ".partial"
	rows, err := record.Open[Result](partialPath)
	if err != nil {
		return Cell{}, err
	}

	cfg.Log.Info("running cell", "cell", id, "driver", load.Driver, "load", load.String(), "duration", cfg.CellDuration)
	started := time.Now()
	watcher := Watch(ctx, cfg.Contamination)
	// Opened when the measured window opens rather than at the cell's first
	// instant, so the prefix cache hit rate on a row covers the same requests the
	// goodput beside it does. See prefixCacheWindow for how closely.
	prefixCache := watchPrefixCache(ctx, cfg.Replicas, cfg.Warmup)

	driver := DriverConfig{
		Target:      cfg.Target,
		Concurrency: load.Concurrency,
		ArrivalRate: load.ArrivalRate,
		ThinkTime:   cfg.ThinkTime,
		Duration:    cfg.CellDuration,
		Warmup:      cfg.Warmup,
		// Each cell gets its own slice of the workload's user space, keyed on
		// the axes it varies and not on the policy. Two cells that differ only
		// in repetition or load level therefore send different bytes — without
		// it the second repetition re-sends the first one's prompts and reads
		// them back out of the replica's prefix cache — while the same cell
		// under two policies sends identical bytes, which is what makes the
		// policies comparable at all.
		//
		// The pressure point is a third axis, added because the grid runs every
		// one of its points at one concurrency and one set of repetitions: those
		// two terms alone are identical across the whole grid, so without this
		// each point would re-send the previous point's conversations wherever
		// their session pools overlap. See GridWorkloadOffset. A cell that
		// states no working set adds nothing here and sends exactly the bytes it
		// always did.
		Workload: Shifted(cfg.Workload, CellWorkloadOffset(load, repetition)+cfg.gridOffset),
		Rows:     rows,
		Labels:   Labels{CellID: id, Policy: cfg.Policy, Repetition: repetition},
		Log:      cfg.Log,
	}
	var results []Result
	var runErr error
	if load.Driver == OpenLoopDriver {
		results, runErr = RunOpenLoop(ctx, driver)
	} else {
		results, runErr = RunClosedLoop(ctx, driver)
	}
	contamination := watcher.Stop()
	served := prefixCache.Stop(ctx)
	// The index is read after the cell rather than before it, because the
	// question is how much belief the run built up and not how much it started
	// with.
	after, afterRead := readRouterStats(ctx, cfg)
	var index prefix.Stats
	if after.PrefixIndex != nil {
		index = *after.PrefixIndex
	}
	ejections, ejectionsRead := 0, beforeRead && afterRead
	if ejectionsRead {
		ejections = totalEjections(after) - totalEjections(before)
	}
	exact := residencyOver(before, after, beforeRead && afterRead)
	ended := time.Now()

	if closeErr := rows.Close(); closeErr != nil && runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return Cell{}, fmt.Errorf("bench: cell %s: %w", id, runErr)
	}
	// An interrupted cell is not a cell. Its rows stay on disk under the
	// visibly incomplete .partial name, and neither they nor a cell record are
	// renamed into place — otherwise the next pass would find a cache entry for
	// a cell that was cut off partway and never re-run it.
	if err := ctx.Err(); err != nil {
		cfg.Log.Warn("cell was interrupted and will be re-run, not cached", "cell", id, "partial_rows", partialPath)
		return Cell{}, fmt.Errorf("bench: cell %s was interrupted: %w", id, err)
	}
	if err := os.Rename(partialPath, rowPath); err != nil {
		return Cell{}, fmt.Errorf("bench: cell %s: %w", id, err)
	}

	cell := Cell{
		ID:          id,
		Policy:      cfg.Policy,
		Driver:      load.Driver,
		Concurrency: load.Concurrency,
		ArrivalRate: load.ArrivalRate,
		Repetition:  repetition,
		Workload:    cfg.Workload.Name(),
		WorkingSet:  OfferedWorkingSet(cfg.Workload),
		Skew:        OfferedSkew(cfg.Workload),

		KVHighWater:         cfg.Spill.KVHighWater,
		LoadImbalanceFactor: cfg.Spill.LoadImbalanceFactor,

		HashLeadingBlocks: cfg.HashPoint.LeadingBlocks,
		HashWeight:        cfg.HashPoint.HashWeight,

		ArrivalPlan:    arrivalPlanFor(load),
		ThinkTimeNs:    thinkTimeFor(load, cfg.ThinkTime).Nanoseconds(),
		KVEvents:       cfg.FleetKVEvents,
		CellDurationNs: cfg.CellDuration.Nanoseconds(),
		WarmupNs:       cfg.Warmup.Nanoseconds(),
		StartedAtNs:    started.UnixNano(),
		EndedAtNs:      ended.UnixNano(),

		PrefixCacheHits:    served.Cache.Hits,
		PrefixCacheQueries: served.Cache.Queries,
		PrefixCacheRead:    served.Cache.Read,

		PromptTokens:       served.Prefill.PromptTokens,
		PromptTokensCached: served.Prefill.CachedTokens,
		PrefillRead:        served.Prefill.Read,

		PrefixIndexNodes: index.Nodes,
		PrefixIndexCap:   index.NodeCap,

		Ejections:     ejections,
		EjectionsRead: ejectionsRead,
		OutOfRotation: outOfRotation(after),

		ResidencyLost:         exact.lost,
		ResidencyResets:       exact.resets,
		ResidencyReconnects:   exact.reconnects,
		ResidencyOrphaned:     exact.orphaned,
		ResidencyRead:         exact.read,
		ResidencyDisconnected: exact.disconnected,

		Summary:       Summarize(results, cfg.summaryOptions()),
		Contamination: contamination,
	}
	flagContamination(&cell)
	flagFleetChanges(&cell)
	flagResidency(&cell)

	if err := writeCell(cellDir, cell); err != nil {
		return Cell{}, err
	}
	cfg.Log.Info("cell complete", "cell", id, "driver", cell.Driver,
		"requests", cell.Requests, "successes", cell.Successes,
		"dropped", cell.Dropped, "failed", cell.Failed, "slo_violations", cell.SLOViolations,
		"goodput_rps", cell.GoodputRPS, "clean", cell.Clean, "flagged", cell.Flagged)
	return cell, nil
}

// flagContamination adds the reasons the GPUs give for not averaging this cell
// in with the others. Summarize flags what the rows say; this flags what the
// cards said.
func flagContamination(cell *Cell) {
	for _, reason := range cell.Contamination.Reasons() {
		cell.Flag(reason)
	}
}

// flagFleetChanges adds the reasons a cell that did not run on the whole fleet
// must not be averaged in with the others: a replica ejected during it, or one
// out of rotation when it ended. Like the contamination evidence these come from
// the cell's record rather than its rows, so they are reapplied whenever the
// cell is resummarised.
func flagFleetChanges(cell *Cell) {
	if cell.Ejections > 0 {
		cell.Flag(fmt.Sprintf("the router ejected a replica %d time(s) during this cell, so it measured a smaller fleet for part of its window; "+
			"nothing was dropped to show it, because the router reroutes around a missing replica. Re-run it", cell.Ejections))
	}
	if len(cell.OutOfRotation) > 0 {
		cell.Flag(fmt.Sprintf("the router was not routing to %s when this cell ended, so it measured a smaller fleet for at least its tail. Re-run it once the fleet is whole",
			strings.Join(cell.OutOfRotation, ", ")))
	}
}

// flagResidency adds the reasons a cell run while exact residency's view of the
// caches was incomplete must not be averaged in. Like the fleet changes these
// come from the cell's record rather than its rows, so they are reapplied
// whenever the cell is resummarised.
func flagResidency(cell *Cell) {
	if cell.ResidencyLost > 0 || cell.ResidencyResets > 0 {
		cell.Flag(fmt.Sprintf("exact residency lost %d batches of the engines' KV cache events during this cell and reset %d time(s), so its index knew less than it claimed for part of the window (ADR-0010). Re-run it",
			cell.ResidencyLost, cell.ResidencyResets))
	}
	if cell.ResidencyReconnects > 0 {
		cell.Flag(fmt.Sprintf("exact residency's event stream reconnected %d time(s) during this cell, and each reconnect starts a replica's residency from nothing (ADR-0010). Re-run it",
			cell.ResidencyReconnects))
	}
	if len(cell.ResidencyDisconnected) > 0 {
		cell.Flag(fmt.Sprintf("exact residency's event stream from %s was not connected when this cell ended, so its index was following nothing for that replica. Re-run it once the stream is back",
			strings.Join(cell.ResidencyDisconnected, ", ")))
	}
}

// residencyWindow is what exact residency's streams went through between two
// readings of the router's stats.
type residencyWindow struct {
	lost, resets, reconnects, orphaned uint64
	read                               bool
	disconnected                       []string
}

// residencyOver is the residency window between the two readings either side of
// a cell. Nothing is read unless both readings answered and both carried
// residency, which only exact residency's router reports.
func residencyOver(before, after router.Stats, read bool) residencyWindow {
	if !read || len(before.Residency) == 0 || len(after.Residency) == 0 {
		return residencyWindow{}
	}
	b, a := residencyTotals(before), residencyTotals(after)
	w := residencyWindow{
		read:       true,
		lost:       grownBy(a.lost, b.lost),
		resets:     grownBy(a.resets, b.resets),
		reconnects: grownBy(a.connections, b.connections),
		orphaned:   grownBy(a.orphaned, b.orphaned),
	}
	for _, r := range after.Residency {
		if !r.Stream.Connected {
			w.disconnected = append(w.disconnected, r.Replica)
		}
	}
	return w
}

// residencyCounts is the router's cumulative residency counters, summed across
// its replicas: what one reading says, where a residencyWindow is what two say.
type residencyCounts struct {
	lost, resets, connections, orphaned uint64
}

// residencyTotals sums the router's residency counters across its replicas.
func residencyTotals(stats router.Stats) residencyCounts {
	var t residencyCounts
	for _, r := range stats.Residency {
		t.lost += r.Stream.Lost
		t.resets += r.Stream.Resets
		t.connections += r.Stream.Connections
		t.orphaned += r.Orphaned
	}
	return t
}

// grownBy is how far a cumulative counter moved between two readings. A counter
// that went backwards belongs to a router that restarted between them, which
// counts from zero again, so everything it now reports happened in the window.
func grownBy(after, before uint64) uint64 {
	if after >= before {
		return after - before
	}
	return after
}

// outOfRotation names the replicas a router's stats say it is not routing to.
func outOfRotation(stats router.Stats) []string {
	var out []string
	for _, r := range stats.Replicas {
		if !r.InRotation() {
			out = append(out, r.ID)
		}
	}
	return out
}

// loadCell reads a completed cell, if there is one.
func loadCell(cellDir, id string) (Cell, bool) {
	contents, err := os.ReadFile(filepath.Join(cellDir, id+".json"))
	if err != nil {
		return Cell{}, false
	}
	var cell Cell
	if err := json.Unmarshal(contents, &cell); err != nil {
		// A truncated record is not a cached cell. Re-running costs one cell;
		// trusting a half-written one costs the result.
		return Cell{}, false
	}
	return cell, true
}

// matchesSLO reports whether a cached cell was summarised against the SLO now
// configured.
func (c Cell) matchesSLO(slo SLO) bool {
	return c.SLOTTFTNs == slo.TTFT.Nanoseconds() && c.SLOITLNs == slo.ITL.Nanoseconds()
}

// resummarize recomputes a cached cell's summary from its rows under the
// current SLO, keeping the contamination evidence the original run gathered.
// The rows are the system of record precisely so this is possible.
func resummarize(cellDir string, cached Cell, cfg SweepConfig) (Cell, error) {
	rows, err := decodeFile[Result](filepath.Join(cellDir, cached.ID+".jsonl"))
	if err != nil {
		return Cell{}, err
	}
	cached.Summary = Summarize(rows, cfg.summaryOptions())
	flagContamination(&cached)
	flagFleetChanges(&cached)
	flagResidency(&cached)
	if err := writeCell(cellDir, cached); err != nil {
		return Cell{}, err
	}
	return cached, nil
}

// discard moves a contaminated cell's record and rows out of the cell
// directory, so the cell is recomputed while the evidence for why it was thrown
// away survives. It lands outside cells/ so compaction cannot pick it up as
// though it were a result.
func discard(dir, cellDir, id string) error {
	graveyard := filepath.Join(dir, "discarded")
	if err := os.MkdirAll(graveyard, 0o755); err != nil {
		return fmt.Errorf("bench: create %s: %w", graveyard, err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, ext := range []string{".json", ".jsonl"} {
		from := filepath.Join(cellDir, id+ext)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		to := filepath.Join(graveyard, id+"-"+stamp+ext)
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("bench: discard %s: %w", from, err)
		}
	}
	return nil
}

// writeCell writes the record that marks a cell complete. It is written last
// and renamed into place, so its presence means the cell finished.
func writeCell(cellDir string, cell Cell) error {
	contents, err := json.MarshalIndent(cell, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: marshal cell %s: %w", cell.ID, err)
	}
	path := filepath.Join(cellDir, cell.ID+".json")
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("bench: write cell %s: %w", cell.ID, err)
	}
	return os.Rename(tmp, path)
}
