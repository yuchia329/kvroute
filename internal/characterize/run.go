package characterize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/gpu"
	"github.com/yuchia329/kvroute/internal/record"
)

// DefaultLevels are the two load levels each replica is driven at.
//
// One, because that is where the hardware floor lives and where the SLO comes
// from.
//
// Thirty-two, because that is where idea.md §10 puts the risk. It locates the
// NUMA starvation at a *fleet* concurrency of 128–256, which across six
// replicas is 21–43 requests each, and a probe drives one replica: sixteen
// would be a fleet at 96, below the range the concern is stated for. An
// asymmetry in host-side CPU work cannot show at concurrency one either, where
// there is no queue and no batch to be starved of, so the second level is the
// only one that can test it at all.
var DefaultLevels = []int{1, 32}

// Schedule is whether a probe had the host to itself.
//
// It is the difference between two questions that look like one. Solo asks what
// a replica does; together asks what a replica does while five others are doing
// the same thing, which is the only condition under which host-side contention
// exists at all.
type Schedule string

const (
	// ScheduleSolo drives one replica at a time, every other replica idle. It
	// is what idea.md §10 prescribes, and it isolates a replica from the fleet:
	// what it finds is a property of the card and its slot.
	ScheduleSolo Schedule = "solo"
	// ScheduleTogether drives every replica at once under matched load.
	//
	// This is the schedule §10's own hypothesis needs and its method removes.
	// The claim is that GPUs 0-3 get 12 host threads each against 4-5's 24, and
	// that ratio only exists while all four cards on the crowded node are busy.
	// Driven alone, a replica on either node has its whole node's threads, so a
	// solo pass cannot produce the starvation it is looking for.
	ScheduleTogether Schedule = "together"
)

// Config configures a characterization run.
type Config struct {
	// Dir is where the rows and the record land.
	Dir      string
	Replicas []fleet.Replica
	Model    string

	// Schedule is whether replicas are driven one at a time or all at once.
	// Defaults to ScheduleSolo.
	Schedule Schedule
	// Levels are the load levels each replica is driven at.
	Levels []int
	// Repetitions is how many times the whole pass is repeated. More than one
	// is what makes the replica order alternate, which is what keeps a host
	// that drifts over the run from looking like a fleet ordered by index.
	Repetitions int
	// ProbeDuration is how long each replica is driven for at each level, and
	// Warmup is the slice at the start of that which is recorded but not
	// summarised.
	ProbeDuration time.Duration
	Warmup        time.Duration
	// ReplicaWarmup is how many requests each replica is sent, directly, before
	// the first probe, so no replica serves its first real forward pass inside
	// the measurement the SLO is derived from.
	ReplicaWarmup int
	// Settle is the pause between probes, so one probe's tail stays out of the
	// next one's window.
	Settle time.Duration

	Workload bench.Workload

	Estimate      Estimate
	SessionTokens int
	SLOMultiple   float64
	Tolerance     float64
	// MaxPrefixHitRate is how much of a probe's prompt work may come out of the
	// replica's prefix cache before the probe is flagged as measuring the cache
	// rather than the hardware. Zero uses MaxFloorPrefixHitRate.
	MaxPrefixHitRate float64

	// EngineSampleInterval is how often each probe asks its replica what it is
	// running and what it has queued. Zero uses DefaultEngineSampleInterval.
	EngineSampleInterval time.Duration

	// Prober reads the host's GPUs, for the topology record and for each
	// probe's contamination evidence. Nil records that neither was checked
	// rather than claiming both were clean.
	Prober        *gpu.Prober
	Contamination bench.ContaminationConfig

	Log *slog.Logger
}

// Probe is one replica driven on its own at one load level.
//
// Deliberately not a cell. A cell is a point of the policy comparison and
// carries a policy; a probe has no router in front of it and no policy to name,
// which is exactly what makes it able to say something about a replica rather
// than about a routing decision.
type Probe struct {
	ID        string `json:"id"`
	Placement `json:"placement"`
	BaseURL   string `json:"base_url"`

	// Schedule is what else was running. A probe's latency means one thing when
	// the rest of the host was idle and another when it was not, so the two are
	// never compared without it.
	Schedule Schedule `json:"schedule"`

	Concurrency int `json:"concurrency"`
	Repetition  int `json:"repetition"`
	// Order is this probe's position in the whole run. It is recorded because
	// the alternating replica order is only useful if the order is visible: a
	// figure that drifts with Order and not with the replica is the host
	// warming up, not a slow card.
	Order int `json:"order"`

	StartedAtNs int64 `json:"started_at_ns"`
	EndedAtNs   int64 `json:"ended_at_ns"`

	// PrefixCache is how much of this probe's prompt work the replica answered
	// out of its cache. A probe that read the cache measured the cache, not the
	// hardware, and it looks exactly like a fast replica if nobody checks.
	PrefixCache PrefixCacheDelta `json:"prefix_cache"`
	// EngineLoad is what the replica said it was actually running and queueing
	// while this probe drove it, as opposed to what the driver offered.
	EngineLoad EngineLoad `json:"engine_load"`

	bench.Summary       `json:"summary"`
	bench.Contamination `json:"contamination"`
}

// ProbeID is a probe's identity: replica, level, repetition. The schedule is
// not in it — a run has one schedule throughout, and it is on the record.
func ProbeID(replicaID string, concurrency, repetition int) string {
	return fmt.Sprintf("%s-c%d-r%d", replicaID, concurrency, repetition)
}

// Characterization is everything one run established. It is the record the
// phase-1 gates are checked against.
type Characterization struct {
	At       time.Time `json:"at"`
	Model    string    `json:"model"`
	Workload string    `json:"workload"`
	// Schedule is how the probes were run, and it changes what every latency
	// here means.
	Schedule Schedule `json:"schedule"`

	Capacity Capacity `json:"capacity"`
	// SessionTokens is the divisor that turns fleet capacity into a resident
	// session count. Recorded because the WS points below mean nothing without
	// it, and a re-derivation has to use the one the run used.
	SessionTokens int          `json:"session_tokens"`
	WorkingSets   []WorkingSet `json:"working_sets"`
	Topology      gpu.Topology `json:"topology"`

	Floor         Floor        `json:"floor"`
	SLO           DerivedSLO   `json:"slo"`
	SLOCandidates []DerivedSLO `json:"slo_candidates"`

	Symmetry Symmetry `json:"symmetry"`
	Probes   []Probe  `json:"probes"`

	// Flagged marks a characterization whose own numbers must not be used
	// without reading why first. Every downstream figure scales off these, so a
	// quietly unusable one is worse here than anywhere else.
	Flagged     bool     `json:"flagged"`
	FlagReasons []string `json:"flag_reasons,omitempty"`
}

// Run establishes the fleet's measured facts.
//
// It is not resumable, unlike the sweep. The whole pass is tens of minutes
// rather than tens of hours, and the facts it establishes have to describe one
// fleet in one state: half of a capacity reading from this morning and half of
// a symmetry verdict from tonight is not a characterization of anything.
func Run(ctx context.Context, cfg Config) (Characterization, error) {
	cfg = cfg.withDefaults()
	if len(cfg.Replicas) == 0 {
		return Characterization{}, errors.New("characterize: at least one replica is required")
	}
	if cfg.Dir == "" {
		return Characterization{}, errors.New("characterize: a directory to write the record to is required")
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return Characterization{}, fmt.Errorf("characterize: create %s: %w", cfg.Dir, err)
	}

	bases := make([]string, 0, len(cfg.Replicas))
	ids := make([]string, 0, len(cfg.Replicas))
	for _, r := range cfg.Replicas {
		bases = append(bases, r.BaseURL)
		ids = append(ids, r.ID)
	}
	if err := bench.CheckReplicas(ctx, bases); err != nil {
		return Characterization{}, err
	}

	c := Characterization{At: time.Now().UTC(), Model: cfg.Model, Workload: cfg.Workload.Name(), Schedule: cfg.Schedule}

	// Capacity and topology first, while the fleet is idle: both are static
	// facts about how the fleet was built, and reading them before any load
	// means the run's own traffic cannot be in them.
	capacity, err := ReadCapacity(ctx, nil, cfg.Replicas, cfg.Estimate)
	if err != nil {
		return Characterization{}, err
	}
	c.Capacity = capacity
	cfg.Log.Info("fleet KV capacity",
		"tokens", capacity.Tokens, "per_replica", capacity.Replicas[0].Tokens,
		"estimated", capacity.EstimatedTokens, "gap", fmt.Sprintf("%+.1f%%", capacity.Gap*100))

	if cfg.Prober != nil {
		if c.Topology, err = cfg.Prober.Topology(ctx); err != nil {
			return Characterization{}, err
		}
		// Verbatim beside the record, so the parse above can be checked against
		// what the driver actually said without decoding JSON to find it.
		if err := os.WriteFile(filepath.Join(cfg.Dir, "topology.txt"), []byte(c.Topology.Printable()), 0o644); err != nil {
			return Characterization{}, fmt.Errorf("characterize: write topology: %w", err)
		}
		for _, group := range c.Topology.NUMAGroups() {
			cfg.Log.Info("NUMA placement", "node", group.Node, "gpus", group.GPUs,
				"threads_per_gpu", group.ThreadsPerGPU)
		}
	}

	if err := bench.WarmReplicas(ctx, bench.WarmConfig{
		Replicas: bases,
		Requests: cfg.ReplicaWarmup,
		Workload: cfg.Workload,
		Log:      cfg.Log,
	}); err != nil {
		return Characterization{}, err
	}

	placements := Placements(ids, c.Topology)
	probes, rows, err := drive(ctx, cfg, placements)
	c.Probes = probes
	if err != nil {
		return c, err
	}

	// Everything from here is derived from the rows, so it is one function that
	// a re-derivation calls too. Splitting the analysis from the measurement is
	// what lets a definition be corrected without spending another hour of GPU
	// time re-measuring a fleet that has not changed.
	c = Analyse(c, rows, cfg.analysisOptions())
	cfg.Log.Info("latency floor measured", "ttft_p50", c.Floor.TTFT(), "itl_p50", c.Floor.ITL(),
		"requests", c.Floor.Successes, "slo", c.SLO.String())

	if err := writeJSON(filepath.Join(cfg.Dir, "characterization.json"), c); err != nil {
		return c, err
	}
	return c, nil
}

// drive runs every probe and returns them with every row they produced.
//
// Probes are grouped by what runs at the same time: one probe per group under
// ScheduleSolo, every replica in one group under ScheduleTogether. Groups run
// one after another either way, so the two schedules differ in a single place
// rather than in two loops that could drift apart.
//
// Under ScheduleSolo the replica order alternates between repetitions. Driving
// one replica at a time is what idea.md §10 asks for, but a sequential pass
// puts replica 0 at the start of every repetition and replica 5 at the end, so
// a host that drifts over the run would produce a latency gradient that reads
// exactly like the NUMA asymmetry being looked for. Reversing on alternate
// repetitions puts each replica at both ends. Under ScheduleTogether there is
// no order to alternate: that is the point.
func drive(ctx context.Context, cfg Config, placements []Placement) ([]Probe, []bench.Result, error) {
	rowPath := filepath.Join(cfg.Dir, "probes.jsonl")
	// Rows are appended, and this pass does not resume, so a second run in the
	// same directory would silently interleave two fleets' rows under one
	// record. Refused rather than truncated: the previous run's rows are the
	// system of record for whatever was published off them.
	if _, err := os.Stat(rowPath); err == nil {
		return nil, nil, fmt.Errorf("characterize: %s already holds a run's rows, and this pass does not resume. Move it aside or point -dir somewhere else", rowPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	writer, err := record.Open[bench.Result](rowPath)
	if err != nil {
		return nil, nil, err
	}
	defer writer.Close()

	var probes []Probe
	var rows []bench.Result
	order := 0

	for _, concurrency := range cfg.Levels {
		for repetition := 1; repetition <= cfg.Repetitions; repetition++ {
			for _, group := range groups(cfg.Schedule, placements, repetition) {
				if err := ctx.Err(); err != nil {
					return probes, rows, err
				}
				if order > 0 && cfg.Settle > 0 {
					time.Sleep(cfg.Settle)
				}

				// One contamination watcher for the group, not one per probe:
				// the probes in a group overlap and would otherwise have six
				// samplers shelling out to nvidia-smi against the same cards,
				// which is load the measurement did not intend to apply.
				watcher := bench.Watch(ctx, cfg.Contamination)
				groupProbes, groupRows, err := driveGroup(ctx, cfg, group, concurrency, repetition, &order, writer)
				contamination := watcher.Stop()
				if err != nil {
					return probes, rows, err
				}
				for i := range groupProbes {
					groupProbes[i].Contamination = contamination
					for _, reason := range contamination.Reasons() {
						groupProbes[i].Flag(reason)
					}
					if reason := groupProbes[i].PrefixCache.reason(cfg.MaxPrefixHitRate); reason != "" {
						groupProbes[i].Flag(reason)
					}
					cfg.Log.Info("probe complete", "probe", groupProbes[i].ID,
						"ttft_p50", time.Duration(groupProbes[i].TTFTP50Ns),
						"itl_p50", time.Duration(groupProbes[i].ITLP50Ns),
						"requests", groupProbes[i].Requests,
						"batch_mean", fmt.Sprintf("%.1f", groupProbes[i].EngineLoad.MeanRunning),
						"queued_max", fmt.Sprintf("%.0f", groupProbes[i].EngineLoad.MaxWaiting),
						"prefix_hit_rate", fmt.Sprintf("%.1f%%", groupProbes[i].PrefixCache.HitRate()*100),
						"clean", groupProbes[i].Clean, "flagged", groupProbes[i].Flagged)
				}
				probes = append(probes, groupProbes...)
				rows = append(rows, groupRows...)
			}
		}
	}
	return probes, rows, writer.Close()
}

// groups is the sets of replicas that are driven at the same time.
func groups(schedule Schedule, placements []Placement, repetition int) [][]Placement {
	if schedule == ScheduleTogether {
		return [][]Placement{placements}
	}
	ordered := passOrder(placements, repetition)
	out := make([][]Placement, 0, len(ordered))
	for _, p := range ordered {
		out = append(out, []Placement{p})
	}
	return out
}

// passOrder reverses the replica order on even repetitions, so no replica is
// always measured first.
func passOrder(placements []Placement, repetition int) []Placement {
	if repetition%2 == 1 {
		return placements
	}
	reversed := make([]Placement, len(placements))
	for i, p := range placements {
		reversed[len(placements)-1-i] = p
	}
	return reversed
}

// driveGroup runs one group's probes at the same time and waits for all of
// them. A group of one is the solo case and costs nothing extra.
func driveGroup(ctx context.Context, cfg Config, group []Placement, concurrency, repetition int, order *int, writer *record.Writer[bench.Result]) ([]Probe, []bench.Result, error) {
	probes := make([]Probe, len(group))
	collected := make([][]bench.Result, len(group))
	errs := make([]error, len(group))

	if len(group) > 1 {
		cfg.Log.Info("probing replicas together", "replicas", len(group),
			"concurrency", concurrency, "repetition", repetition, "duration", cfg.ProbeDuration)
	}

	var wg sync.WaitGroup
	for i, p := range group {
		// Assigned before launching, so every probe in the run has its own
		// slice of the workload's user space whether or not it shares a start
		// time with another.
		*order++
		at := *order
		wg.Add(1)
		go func() {
			defer wg.Done()
			probes[i], collected[i], errs[i] = runProbe(ctx, cfg, p, concurrency, repetition, at, writer)
		}()
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	var rows []bench.Result
	for _, got := range collected {
		rows = append(rows, got...)
	}
	return probes, rows, nil
}

func runProbe(ctx context.Context, cfg Config, p Placement, concurrency, repetition, order int, rows *record.Writer[bench.Result]) (Probe, []bench.Result, error) {
	var replica fleet.Replica
	for _, r := range cfg.Replicas {
		if r.ID == p.ReplicaID {
			replica = r
		}
	}
	id := ProbeID(p.ReplicaID, concurrency, repetition)

	if cfg.Schedule == ScheduleSolo {
		cfg.Log.Info("probing replica", "probe", id, "gpu", p.GPUIndex, "numa", p.NUMANode,
			"concurrency", concurrency, "duration", cfg.ProbeDuration)
	}
	started := time.Now()
	before := readPrefixCounters(ctx, nil, replica)
	engine := watchEngineLoad(ctx, nil, replica, cfg.EngineSampleInterval)

	results, err := bench.RunClosedLoop(ctx, bench.DriverConfig{
		Target:        replica.BaseURL,
		DirectReplica: p.ReplicaID,
		Concurrency:   concurrency,
		Duration:      cfg.ProbeDuration,
		Warmup:        cfg.Warmup,
		// Every probe sends from its own slice of the workload's user space, so
		// no probe re-sends another's prompts. Without it the second repetition
		// re-sends the first's, the replica answers them out of its prefix
		// cache, and the floor drops sevenfold for a reason that has nothing to
		// do with the hardware it claims to describe.
		Workload: bench.Shifted(cfg.Workload, order*bench.WorkloadStride),
		Rows:     rows,
		// No policy: nothing routed these requests, which is the point. The
		// cell id is the probe's, so a row can still be traced to what produced
		// it after the files are concatenated.
		Labels: bench.Labels{CellID: id, Repetition: repetition},
		Log:    cfg.Log,
	})
	load := engine.Stop()
	ended := time.Now()
	if err != nil {
		return Probe{}, nil, fmt.Errorf("characterize: probe %s: %w", id, err)
	}
	prefixCache := readPrefixCounters(ctx, nil, replica).since(before)

	probe := Probe{
		ID:          id,
		Placement:   p,
		BaseURL:     replica.BaseURL,
		Schedule:    cfg.Schedule,
		Concurrency: concurrency,
		Repetition:  repetition,
		Order:       order,
		StartedAtNs: started.UnixNano(),
		EndedAtNs:   ended.UnixNano(),
		PrefixCache: prefixCache,
		EngineLoad:  load,
		Summary:     bench.Summarize(results, bench.SummaryOptions{}),
	}
	return probe, results, nil
}

// flaggedProbes collapses the probes' flag reasons into one line per kind of
// reason, naming how many probes carried it and a few of them.
//
// Grouped by kind rather than by the sentence, because the sentences carry the
// numbers that made them fire: four probes that were all still warming up say
// so with four different percentages in them, and grouping on the whole string
// would print "1 of 36 probes flagged" four times over.
func flaggedProbes(probes []Probe) []string {
	var order []string
	carriers := map[string][]string{}
	examples := map[string]string{}
	for _, probe := range probes {
		for _, reason := range probe.FlagReasons {
			kind, _, _ := strings.Cut(reason, ":")
			if _, seen := carriers[kind]; !seen {
				order = append(order, kind)
				examples[kind] = reason
			}
			carriers[kind] = append(carriers[kind], probe.ID)
		}
	}

	out := make([]string, 0, len(order))
	for _, kind := range order {
		ids := carriers[kind]
		named, suffix := ids, ""
		if len(named) > 3 {
			named, suffix = named[:3], fmt.Sprintf(" and %d more", len(ids)-3)
		}
		out = append(out, fmt.Sprintf("%d of %d probes flagged: %s (%s%s)",
			len(ids), len(probes), examples[kind], strings.Join(named, ", "), suffix))
	}
	return out
}

// rowsAt returns the rows taken at one load level.
func rowsAt(rows []bench.Result, concurrency int) []bench.Result {
	var out []bench.Result
	for _, r := range rows {
		if r.Concurrency == concurrency {
			out = append(out, r)
		}
	}
	return out
}

// AnalysisOptions are the judgements the analysis applies to the rows: the
// thresholds and the multiple. They are recorded so a re-derivation applies the
// same ones the run did unless it is deliberately changing them.
type AnalysisOptions struct {
	SessionTokens    int
	SLOMultiple      float64
	Tolerance        float64
	MaxPrefixHitRate float64
}

func (cfg Config) analysisOptions() AnalysisOptions {
	return AnalysisOptions{
		SessionTokens:    cfg.SessionTokens,
		SLOMultiple:      cfg.SLOMultiple,
		Tolerance:        cfg.Tolerance,
		MaxPrefixHitRate: cfg.MaxPrefixHitRate,
	}
}

// Options reads the analysis back off a record, so re-deriving it applies what
// the run applied rather than whatever the defaults happen to be now.
func (c Characterization) Options() AnalysisOptions {
	return AnalysisOptions{
		SessionTokens:    c.SessionTokens,
		SLOMultiple:      c.SLO.Multiple,
		Tolerance:        c.Symmetry.Tolerance,
		MaxPrefixHitRate: c.Floor.MaxPrefixHitRate,
	}
}

// Analyse computes everything the record says beyond what was measured
// directly: the working set rescaling, the latency floor, the SLO derived from
// it, the symmetry verdict, and every flag.
//
// One pass, three answers. The concurrency-1 rows pooled across replicas are
// the hardware floor; the same rows split by replica are half the symmetry
// comparison; the SLO follows from the floor. Measuring them separately would
// derive an SLO from a fleet in one state and judge symmetry on a fleet in
// another.
//
// It rebuilds the flags from scratch rather than adding to them, so running it
// twice on the same record says the same thing twice.
func Analyse(c Characterization, rows []bench.Result, opts AnalysisOptions) Characterization {
	if opts.SessionTokens <= 0 {
		opts.SessionTokens = DefaultSessionTokens
	}
	if opts.SLOMultiple <= 0 {
		opts.SLOMultiple = DefaultSLOMultiple
	}
	if opts.Tolerance <= 0 {
		opts.Tolerance = DefaultSymmetryTolerance
	}
	if opts.MaxPrefixHitRate <= 0 {
		opts.MaxPrefixHitRate = MaxFloorPrefixHitRate
	}
	c.Flagged, c.FlagReasons = false, nil
	c.SessionTokens = opts.SessionTokens
	c.WorkingSets = c.Capacity.WorkingSets(opts.SessionTokens, WorkingSetPoints)

	if len(c.Capacity.Replicas) > 0 && !c.Capacity.Uniform {
		c.flag("the replicas do not report the same KV capacity, so the fleet aggregate hides a difference between cards")
	}
	if len(c.Topology.GPUs) == 0 {
		c.flag("the host topology was not recorded, so no symmetry result can be read against it")
	}

	c.Floor = NewFloor(rowsAt(rows, 1), len(c.Capacity.Replicas),
		PoolPrefixCache(c.Probes, func(p Probe) bool { return p.Concurrency == 1 }), opts.MaxPrefixHitRate)
	if usable, why := c.Floor.Usable(); !usable {
		c.flag("the latency floor is not usable, so the SLO derived from it is not either: " + why)
	}
	c.SLO = c.Floor.Derive(opts.SLOMultiple)
	c.SLOCandidates = c.Floor.Candidates(SLOCandidateMultiples)

	replicaIDs := make([]string, 0, len(c.Capacity.Replicas))
	for _, r := range c.Capacity.Replicas {
		replicaIDs = append(replicaIDs, r.ReplicaID)
	}
	c.Symmetry = CompareReplicas(rows, Placements(replicaIDs, c.Topology), c.Topology, opts.Tolerance)
	switch {
	case c.Symmetry.Escalation != EscalationNone:
		c.flag(fmt.Sprintf("the replicas are not interchangeable, so §10's escalation is due (%s): %s",
			c.Symmetry.Escalation, strings.Join(c.Symmetry.Findings, "; ")))
	case !c.Symmetry.Resolved:
		// Not the same thing as a difference. The fleet may be even or it may
		// not; this run could not tell, and a phase gate that reads "no
		// escalation needed" off it would be reading nothing.
		c.flag("replica symmetry is unresolved at one or more levels: " + strings.Join(c.Symmetry.Findings, "; "))
	}
	for _, reason := range flaggedProbes(c.Probes) {
		c.flag(reason)
	}
	return c
}

func (c *Characterization) flag(reason string) {
	c.Flagged = true
	c.FlagReasons = append(c.FlagReasons, reason)
}

func (cfg Config) withDefaults() Config {
	if cfg.Schedule == "" {
		cfg.Schedule = ScheduleSolo
	}
	if len(cfg.Levels) == 0 {
		cfg.Levels = DefaultLevels
	}
	if cfg.Repetitions <= 0 {
		cfg.Repetitions = 1
	}
	if cfg.ProbeDuration <= 0 {
		cfg.ProbeDuration = 30 * time.Second
	}
	if cfg.Workload == nil {
		cfg.Workload = bench.NewFixedWorkload(bench.FixedWorkload{Model: cfg.Model})
	}
	if cfg.Estimate == (Estimate{}) {
		cfg.Estimate = HandComputed
	}
	if cfg.SessionTokens <= 0 {
		cfg.SessionTokens = DefaultSessionTokens
	}
	if cfg.SLOMultiple <= 0 {
		cfg.SLOMultiple = DefaultSLOMultiple
	}
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = DefaultSymmetryTolerance
	}
	if cfg.MaxPrefixHitRate <= 0 {
		cfg.MaxPrefixHitRate = MaxFloorPrefixHitRate
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	cfg.Contamination.Log = cfg.Log
	return cfg
}

func writeJSON(path string, v any) error {
	contents, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("characterize: marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("characterize: write %s: %w", path, err)
	}
	return nil
}
