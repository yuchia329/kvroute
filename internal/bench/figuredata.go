package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFigure writes one figure's data to path as JSON, creating its directory.
func WriteFigure(path string, figure any) error {
	encoded, err := json.MarshalIndent(figure, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: encode figure data for %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("bench: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("bench: write %s: %w", path, err)
	}
	return nil
}

// Figure data is every number a plot draws, computed here and written as JSON
// for the plotting script to read.
//
// The split is the point. Percentiles, medians, deltas and what the repetitions
// can support each have one definition, in this package, and the published
// tables are rendered from them. A plotting script that recomputed them from the
// rows would be a second definition, and a figure could then disagree with the
// table printed beside it. So the script places marks and does no arithmetic of
// its own.

// FigureSLO is the SLO in the unit a plot labels it in.
type FigureSLO struct {
	TTFTMs float64 `json:"ttft_ms"`
	ITLMs  float64 `json:"itl_ms"`
}

func figureSLO(s SLO) FigureSLO {
	return FigureSLO{TTFTMs: milliseconds(s.TTFT.Nanoseconds()), ITLMs: milliseconds(s.ITL.Nanoseconds())}
}

func milliseconds(ns int64) float64 {
	return float64(ns) / float64(time.Millisecond)
}

func microseconds(ns int64) float64 {
	return float64(ns) / float64(time.Microsecond)
}

func seconds(ns int64) float64 {
	return float64(ns) / float64(time.Second)
}

// orEmpty is a list as JSON a script can iterate without checking for null.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ComparisonFigure is what the goodput-against-load plot and the cache plot
// draw: one point per policy per load that has a usable cell.
type ComparisonFigure struct {
	SLO      FigureSLO     `json:"slo"`
	Workload string        `json:"workload"`
	Policies []string      `json:"policies"`
	Points   []PolicyPoint `json:"points"`
	Excluded []string      `json:"excluded"`
	Surfaced []string      `json:"surfaced"`
}

// PolicyPoint is one policy at one load, as a plot draws it.
//
// A policy with no usable cell at a load has no point rather than a point at
// zero: it did not score nothing there, it did not run.
type PolicyPoint struct {
	Policy    string  `json:"policy"`
	Driver    string  `json:"driver"`
	Load      float64 `json:"load"`
	LoadLabel string  `json:"load_label"`

	Repetitions   int     `json:"repetitions"`
	GoodputMedian float64 `json:"goodput_median"`
	GoodputMin    float64 `json:"goodput_min"`
	GoodputMax    float64 `json:"goodput_max"`
	// OverFailureThreshold is how many of the repetitions dropped or failed more
	// requests than the threshold allows; a plot marks the point when it is not
	// zero.
	OverFailureThreshold int `json:"over_failure_threshold"`

	// Every figure below is null where what it is made of was not measured, for
	// the reason the table prints an em dash: an unread counter is not a cache
	// that never hit, or a fleet that computed nothing, and a cell with no
	// percentiles is not one that answered instantly.
	TTFTP50Ms *float64 `json:"ttft_p50_ms"`
	TTFTP90Ms *float64 `json:"ttft_p90_ms"`
	TTFTP99Ms *float64 `json:"ttft_p99_ms"`

	PrefixCacheHitRate *float64 `json:"prefix_cache_hit_rate"`
	RecomputedPrefill  *float64 `json:"recomputed_prefill"`
	RedundantPrefill   *float64 `json:"redundant_prefill"`
}

// Figure is the comparison's figure data.
func (c Comparison) Figure() ComparisonFigure {
	f := ComparisonFigure{
		SLO:      figureSLO(c.SLO),
		Workload: c.Workload,
		Policies: orEmpty(c.Policies),
		Points:   []PolicyPoint{},
		Excluded: orEmpty(c.Excluded),
		Surfaced: orEmpty(c.Surfaced),
	}
	for _, row := range c.Rows {
		for _, name := range c.Policies {
			if p, ok := row.point(name); ok {
				f.Points = append(f.Points, p)
			}
		}
	}
	return f
}

// point is one policy's figure data at this row, if it has a usable cell here.
func (r ComparisonRow) point(name string) (PolicyPoint, bool) {
	g, ok := r.Goodput[name]
	if !ok {
		return PolicyPoint{}, false
	}
	p := PolicyPoint{
		Policy:               name,
		Driver:               r.Load.Driver.Name(),
		Load:                 loadValue(r.Load),
		LoadLabel:            r.Load.String(),
		Repetitions:          g.Repetitions,
		GoodputMedian:        g.MedianRPS,
		GoodputMin:           g.MinRPS,
		GoodputMax:           g.MaxRPS,
		OverFailureThreshold: g.OverFailureThreshold,
	}
	if l, ok := r.Latency[name]; ok {
		p.TTFTP50Ms, p.TTFTP90Ms, p.TTFTP99Ms = ref(milliseconds(l.P50Ns)), ref(milliseconds(l.P90Ns)), ref(milliseconds(l.P99Ns))
	}
	if cache := r.PrefixCache[name]; cache.Evidenced() {
		p.PrefixCacheHitRate = ref(cache.HitRate())
	}
	if prefill := r.Prefill[name]; prefill.Evidenced() {
		p.RecomputedPrefill = ref(prefill.Recomputed())
	}
	if excess, ok := r.Redundant(name); ok {
		p.RedundantPrefill = ref(excess)
	}
	return p, true
}

// loadValue is where a load sits on its own axis: users, or requests per second.
func loadValue(l Load) float64 {
	if l.Driver == OpenLoopDriver {
		return l.ArrivalRate
	}
	return float64(l.Concurrency)
}

func ref(v float64) *float64 { return &v }

// PressureMapFigure is what the headline figure draws: the delta at each point of
// the working set × skew grid, and the goodput each policy scored there.
type PressureMapFigure struct {
	Baseline    string            `json:"baseline"`
	Challenger  string            `json:"challenger"`
	SLO         FigureSLO         `json:"slo"`
	Policies    []string          `json:"policies"`
	WorkingSets []float64         `json:"working_sets"`
	Skews       []float64         `json:"skews"`
	Deltas      []GridDelta       `json:"deltas"`
	Goodput     []GridPolicyPoint `json:"goodput"`
	// Missing, Refused, Excluded and Surfaced are what the figure does not rest
	// on, or rests on marked — printed with it, as the report prints them.
	Missing  []string `json:"missing"`
	Refused  []string `json:"refused"`
	Excluded []string `json:"excluded"`
	Surfaced []string `json:"surfaced"`
}

// GridDelta is the headline difference at one point of the grid, at one load.
//
// Label is the delta exactly as the map's table prints it, qualified by what the
// repetitions support, so the figure and the table say the same thing in the
// same words. Separated is the one field that licenses colouring a cell as a
// difference.
type GridDelta struct {
	WorkingSet    float64 `json:"working_set"`
	Skew          float64 `json:"skew"`
	LoadLabel     string  `json:"load_label"`
	Measured      bool    `json:"measured"`
	Replicated    bool    `json:"replicated"`
	Separated     bool    `json:"separated"`
	BaselineZero  bool    `json:"baseline_zero"`
	PercentChange float64 `json:"percent_change"`
	// OverFailureThreshold is whether the delta rests on a repetition that
	// dropped or failed more requests than the threshold allows; Label then
	// carries the ⚠ the table prints.
	OverFailureThreshold bool   `json:"over_failure_threshold"`
	Label                string `json:"label"`
}

// GridPolicyPoint is one policy's figure data at one point of the grid.
type GridPolicyPoint struct {
	WorkingSet float64 `json:"working_set"`
	Skew       float64 `json:"skew"`
	PolicyPoint
}

// Figure is the pressure map's figure data.
func (m PressureMap) Figure() PressureMapFigure {
	f := PressureMapFigure{
		Baseline:    m.Baseline,
		Challenger:  m.Challenger,
		SLO:         figureSLO(m.SLO),
		Policies:    orEmpty(m.Policies),
		WorkingSets: m.workingSetAxis(),
		Skews:       m.skewAxis(),
		Deltas:      []GridDelta{},
		Goodput:     []GridPolicyPoint{},
		Missing:     []string{},
		Refused:     []string{},
		Excluded:    orEmpty(m.Excluded),
		Surfaced:    orEmpty(m.Surfaced),
	}
	for _, load := range m.Loads() {
		for _, point := range m.Points {
			d := point.DeltaAt(load, m.Baseline, m.Challenger)
			f.Deltas = append(f.Deltas, GridDelta{
				WorkingSet:           point.At.WorkingSet,
				Skew:                 point.At.Skew,
				LoadLabel:            load.String(),
				Measured:             d.Measured,
				Replicated:           d.Replicated,
				Separated:            d.Separated,
				BaselineZero:         d.BaselineZero,
				PercentChange:        d.PercentChange,
				OverFailureThreshold: d.OverFailureThreshold,
				Label:                d.String(),
			})
		}
	}
	for _, point := range m.Points {
		for _, row := range point.Comparison.Rows {
			for _, name := range point.Comparison.Policies {
				if p, ok := row.point(name); ok {
					f.Goodput = append(f.Goodput, GridPolicyPoint{WorkingSet: point.At.WorkingSet, Skew: point.At.Skew, PolicyPoint: p})
				}
			}
		}
	}
	for _, at := range m.MissingPoints() {
		f.Missing = append(f.Missing, at.String())
	}
	for _, refused := range m.Refused {
		f.Refused = append(f.Refused, fmt.Sprintf("%s: %s", refused.At, refused.Why))
	}
	return f
}

// RecoveryFigure is what the recovery graph draws: each policy's goodput through
// one failure, against time from the fault, with what happened to the replica
// marked along it.
type RecoveryFigure struct {
	Replica     string    `json:"replica"`
	Fault       string    `json:"fault"`
	FaultVerb   string    `json:"fault_verb"`
	ArrivalRate float64   `json:"arrival_rate"`
	SLO         FigureSLO `json:"slo"`
	BucketS     float64   `json:"bucket_s"`
	// RecoverAtS is when the replica was started again, from the fault.
	RecoverAtS float64             `json:"recover_at_s"`
	Tolerance  float64             `json:"tolerance"`
	Runs       []RecoveryRunFigure `json:"runs"`
}

// RecoveryRunFigure is one policy's run of the scenario.
type RecoveryRunFigure struct {
	Policy          string               `json:"policy"`
	Curve           []RecoveryCurvePoint `json:"curve"`
	Events          []RecoveryEvent      `json:"events"`
	BaselineRPS     float64              `json:"baseline_rps"`
	TroughRPS       float64              `json:"trough_rps"`
	TroughAtS       float64              `json:"trough_at_s"`
	DeficitRequests float64              `json:"deficit_requests"`
	// Steady is whether goodput came back to within the tolerance and stayed
	// there; SteadyAtS is when, and is null for a run that never did — which is
	// not a run that recovered at the fault.
	Steady    bool     `json:"steady"`
	SteadyAtS *float64 `json:"steady_at_s"`
	// The drop accounting, in full: a drop count means something only beside the
	// requests the reroute saved.
	Rerouted         int    `json:"rerouted"`
	DroppedMidStream int    `json:"dropped_mid_stream"`
	DroppedUnplaced  int    `json:"dropped_unplaced"`
	DropAccounting   string `json:"drop_accounting"`
}

// RecoveryCurvePoint is one bucket of a curve, in seconds from the fault.
type RecoveryCurvePoint struct {
	AtS        float64 `json:"at_s"`
	GoodputRPS float64 `json:"goodput_rps"`
	Offered    int     `json:"offered"`
	Rerouted   int     `json:"rerouted"`
	Dropped    int     `json:"dropped"`
}

// RecoveryEvent is one thing that happened to the replica, in seconds from the
// fault, with the words a report uses for it.
type RecoveryEvent struct {
	AtS   float64 `json:"at_s"`
	Kind  string  `json:"kind"`
	Label string  `json:"label"`
}

// Figure is the recovery comparison's figure data.
func (c RecoveryComparison) Figure() RecoveryFigure {
	f := RecoveryFigure{Runs: []RecoveryRunFigure{}}
	if len(c.Runs) == 0 {
		return f
	}
	first := c.Runs[0]
	f = RecoveryFigure{
		Runs:        []RecoveryRunFigure{},
		Replica:     first.Replica,
		Fault:       string(first.Fault),
		FaultVerb:   first.Fault.Verb(),
		ArrivalRate: first.ArrivalRate,
		SLO:         figureSLO(first.SLO),
		BucketS:     seconds(first.BucketNs),
		RecoverAtS:  seconds(first.RecoverAtNs - first.FaultAtNs),
		Tolerance:   first.Tolerance,
	}
	for _, s := range c.Runs {
		run := RecoveryRunFigure{
			Policy:           s.Policy,
			Curve:            make([]RecoveryCurvePoint, 0, len(s.Curve)),
			Events:           make([]RecoveryEvent, 0, len(s.Events)),
			BaselineRPS:      s.Recovery.BaselineRPS,
			TroughRPS:        s.Recovery.TroughRPS,
			TroughAtS:        seconds(s.Recovery.TroughAtNs),
			DeficitRequests:  s.Recovery.DeficitRequests,
			Steady:           s.Recovery.Steady,
			Rerouted:         s.Summary.Rerouted,
			DroppedMidStream: s.DroppedMidStream,
			DroppedUnplaced:  s.DroppedUnplaced,
			DropAccounting:   s.DropAccounting(),
		}
		for _, p := range s.Curve {
			run.Curve = append(run.Curve, RecoveryCurvePoint{
				AtS: seconds(p.AtNs), GoodputRPS: p.GoodputRPS, Offered: p.Offered, Rerouted: p.Rerouted, Dropped: p.Dropped,
			})
		}
		for _, e := range s.Events {
			run.Events = append(run.Events, RecoveryEvent{AtS: seconds(e.AtNs), Kind: string(e.Kind), Label: eventLabels[e.Kind]})
		}
		if s.Recovery.Steady {
			run.SteadyAtS = ref(seconds(s.Recovery.SteadyAtNs))
		}
		f.Runs = append(f.Runs, run)
	}
	return f
}
