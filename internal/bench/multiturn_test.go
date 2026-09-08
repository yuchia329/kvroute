package bench_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
)

// measuredFleetCapacity is what the characterization gates read off all six
// replicas (#10). The WS axis is defined against it, so the generator's session
// counts have to be derived from this number rather than from the by-hand
// estimate the fleet beat by 9.8%.
const measuredFleetCapacity = 755712

func multiTurnConfig() bench.MultiTurnWorkload {
	return bench.MultiTurnWorkload{
		Model:           testModel,
		Sessions:        16,
		TurnsPerSession: 4,
		PromptTokens:    448,
		OutputTokens:    64,
		Seed:            7,
	}
}

func newMultiTurn(t *testing.T, cfg bench.MultiTurnWorkload) *bench.MultiTurn {
	t.Helper()
	w, err := bench.NewMultiTurn(cfg)
	if err != nil {
		t.Fatalf("build the generator: %v", err)
	}
	return w
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type sentBody struct {
	Model     string    `json:"model"`
	Messages  []message `json:"messages"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
}

func decode(t *testing.T, turn bench.Turn) sentBody {
	t.Helper()
	var body sentBody
	if err := json.Unmarshal(turn.Body, &body); err != nil {
		t.Fatalf("decode a turn: %v", err)
	}
	return body
}

// renderedBytes is the prompt the replica prefills: every message it was sent,
// which is what the growing history costs.
func renderedBytes(b sentBody) int {
	n := 0
	for _, m := range b.Messages {
		n += len(m.Content)
	}
	return n
}

// Turn N resends turns 1..N-1 verbatim. That is the whole reason a session has
// cache locality to preserve, so it is asserted on the bytes rather than
// assumed: every message of the previous turn has to reappear, in order and
// unchanged, ahead of the new one.
func TestEachTurnResendsTheFullHistoryOfThePriorTurns(t *testing.T) {
	w := newMultiTurn(t, multiTurnConfig())

	first := decode(t, w.Next(0, 0))
	if len(first.Messages) != 1 || first.Messages[0].Role != "user" {
		t.Fatalf("the first turn of a session sent %d messages, wanted one user message", len(first.Messages))
	}

	prev := first
	for k := 1; k < 4; k++ {
		cur := decode(t, w.Next(0, k))
		if want := 2*k + 1; len(cur.Messages) != want {
			t.Fatalf("turn %d sent %d messages, wanted %d", k, len(cur.Messages), want)
		}
		for i, m := range prev.Messages {
			if cur.Messages[i] != m {
				t.Errorf("turn %d rewrote message %d of the history it inherited", k, i)
			}
		}
		// The history has to close the previous turn before opening this one,
		// or the prefix the replica holds is not the prefix being resent.
		if got := cur.Messages[len(prev.Messages)].Role; got != "assistant" {
			t.Errorf("turn %d followed the history with a %q message, wanted the assistant's reply", k, got)
		}
		if got := cur.Messages[len(cur.Messages)-1].Role; got != "user" {
			t.Errorf("turn %d ended on a %q message, wanted the new user turn", k, got)
		}
		prev = cur
	}

	// Turns of one session belong to one session, and the turn after the last
	// one starts a fresh conversation rather than growing this one forever.
	session := w.Next(0, 0).Session
	for k := range 4 {
		if got := w.Next(0, k).Session; got != session {
			t.Errorf("turn %d of the visit belongs to session %q, not %q", k, got, session)
		}
	}
	if next := decode(t, w.Next(0, 4)); len(next.Messages) != 1 {
		t.Errorf("the turn after a session's last sent %d messages, so the history did not reset", len(next.Messages))
	}
}

// A trace has to be reproducible or a re-run is measuring a different workload
// and the comparison between policies is not a comparison.
func TestAFixedSeedReproducesAnIdenticalTrace(t *testing.T) {
	a := newMultiTurn(t, multiTurnConfig())
	b := newMultiTurn(t, multiTurnConfig())

	other := multiTurnConfig()
	other.Seed++
	c := newMultiTurn(t, other)

	differs := false
	for user := range 32 {
		for turn := range 12 {
			x, y, z := a.Next(user, turn), b.Next(user, turn), c.Next(user, turn)
			if string(x.Body) != string(y.Body) || x.Session != y.Session {
				t.Fatalf("user %d turn %d differs between two generators built from the same seed", user, turn)
			}
			if string(x.Body) != string(z.Body) {
				differs = true
			}
		}
	}
	if !differs {
		t.Error("changing the seed did not change a single byte of the trace")
	}
}

// WS is offered session tokens over the measured aggregate KV, so a stated
// ratio has to land on the token volume it names — against the capacity the
// characterization gates measured, not against the estimate.
func TestAStatedWorkingSetRatioOffersTheIntendedTokenVolume(t *testing.T) {
	capacity := characterize.Capacity{Tokens: measuredFleetCapacity}
	points := capacity.WorkingSets(characterize.DefaultSessionTokens, characterize.WorkingSetPoints)

	for _, point := range points {
		cfg := multiTurnConfig()
		cfg.Sessions = 0
		cfg.WorkingSet = point.Ratio
		cfg.CapacityTokens = measuredFleetCapacity
		w := newMultiTurn(t, cfg)

		// The default turn geometry is a 2k session, which is the divisor §2
		// turns a fleet capacity into a resident session count with.
		if w.SessionTokens() != characterize.DefaultSessionTokens {
			t.Fatalf("WS %v: a session is %d tokens, wanted %d", point.Ratio, w.SessionTokens(), characterize.DefaultSessionTokens)
		}
		if w.Sessions() != point.Sessions {
			t.Errorf("WS %v: the generator offers %d sessions, the capacity measurement calls for %d", point.Ratio, w.Sessions(), point.Sessions)
		}
		// Short by less than one session is the rounding; over, or short by
		// more, is the ratio not meaning what it says.
		short := point.Tokens - w.OfferedTokens()
		if short < 0 || short >= w.SessionTokens() {
			t.Errorf("WS %v: offers %d tokens against the %d it names, off by %d", point.Ratio, w.OfferedTokens(), point.Tokens, short)
		}
	}
}

// The token figure the WS arithmetic is built on has to be the size of what is
// actually sent, or the ratio is bookkeeping. A session's last turn carries its
// whole history, so its rendered prompt is the session's footprint less the
// generation still to come.
func TestTheOfferedTokenVolumeIsTheSizeOfWhatIsActuallySent(t *testing.T) {
	cfg := multiTurnConfig()
	w := newMultiTurn(t, cfg)

	last := decode(t, w.Next(0, cfg.TurnsPerSession-1))
	wantTokens := w.SessionTokens() - cfg.OutputTokens
	if got := renderedBytes(last) / w.BytesPerToken(); got != wantTokens {
		t.Errorf("the last turn renders %d tokens, but a session is accounted at %d less its %d of output", got, w.SessionTokens(), cfg.OutputTokens)
	}
}

// A session carrying the shared system prompt spends part of its own budget on
// it rather than adding to it, so turning the sharing knobs does not silently
// move the WS point. The two pressure axes have to be independent or the grid
// is not a grid.
func TestTheSharingKnobsDoNotMoveTheWorkingSetPoint(t *testing.T) {
	plain := newMultiTurn(t, multiTurnConfig())

	shared := multiTurnConfig()
	shared.SystemPromptFraction = 1
	shared.SystemPromptTokens = 128
	shared.BranchFraction = 1
	shared.BranchFamilies = 2
	shared.BranchTurns = 2
	w := newMultiTurn(t, shared)

	if w.SessionTokens() != plain.SessionTokens() {
		t.Errorf("sharing changed the session footprint from %d to %d tokens", plain.SessionTokens(), w.SessionTokens())
	}
	last := decode(t, w.Next(0, shared.TurnsPerSession-1))
	wantTokens := w.SessionTokens() - shared.OutputTokens
	if got := renderedBytes(last) / w.BytesPerToken(); got != wantTokens {
		t.Errorf("a session with a shared system prompt renders %d tokens at its last turn, wanted %d", got, wantTokens)
	}
}

// Skew is a full axis, not a constant: α runs from uniform upward, and the
// concentration it produces has to be monotone in it. The load-imbalance branch
// of the spill rule is what this axis exists to fire.
func TestZipfSkewIsExercisableAcrossItsFullRange(t *testing.T) {
	const sessions = 200

	shares := map[float64]float64{}
	for _, alpha := range []float64{0, 1.0, 1.4} {
		cfg := multiTurnConfig()
		cfg.Sessions = sessions
		cfg.Skew = alpha
		w := newMultiTurn(t, cfg)

		counts, draws := map[string]int{}, 0
		for user := range 400 {
			for visit := range 25 {
				counts[w.Next(user, visit*cfg.TurnsPerSession).Session]++
				draws++
			}
		}
		hottest := 0
		for _, n := range counts {
			hottest = max(hottest, n)
		}
		shares[alpha] = float64(hottest) / float64(draws)
	}

	// α = 0 is uniform: no session runs away with the draws.
	if uniform := 1.0 / sessions; shares[0] > 2*uniform {
		t.Errorf("at α=0 the hottest session took %.4f of the draws, which is not uniform against a %.4f share", shares[0], uniform)
	}
	if !(shares[0] < shares[1.0] && shares[1.0] < shares[1.4]) {
		t.Errorf("concentration is not monotone in α: %.4f at 0, %.4f at 1.0, %.4f at 1.4", shares[0], shares[1.0], shares[1.4])
	}
	if shares[1.4] < 0.05 {
		t.Errorf("at α=1.4 the hottest session took only %.4f of the draws, which is not a skewed workload", shares[1.4])
	}
}

// observe walks the generator until it has seen every session, returning the
// first turn of each. Deterministic in the seed, so it is not a sampling
// argument: the same walk sees the same sessions on every run.
func observe(t *testing.T, w *bench.MultiTurn, cfg bench.MultiTurnWorkload) map[string]sentBody {
	t.Helper()
	// Bounded by the stride, because a user past it belongs to another cell:
	// the sessions there are that cell's sessions, not these.
	seen := map[string]sentBody{}
	for user := range min(40*cfg.Sessions, bench.WorkloadStride) {
		turn := w.Next(user, 0)
		if _, ok := seen[turn.Session]; !ok {
			seen[turn.Session] = decode(t, turn)
		}
	}
	if len(seen) != cfg.Sessions {
		t.Fatalf("the walk saw %d of %d sessions", len(seen), cfg.Sessions)
	}
	return seen
}

// A shared system prompt is the cross-session prefix every policy sees, so the
// fraction of sessions carrying it is a knob rather than a constant.
func TestAConfigurableFractionOfSessionsShareASystemPrompt(t *testing.T) {
	for _, fraction := range []float64{0, 0.3, 1} {
		cfg := multiTurnConfig()
		cfg.Sessions = 200
		cfg.SystemPromptFraction = fraction
		cfg.SystemPromptTokens = 128
		w := newMultiTurn(t, cfg)

		carrying, prompts := 0, map[string]bool{}
		for _, body := range observe(t, w, cfg) {
			if body.Messages[0].Role != "system" {
				continue
			}
			carrying++
			prompts[body.Messages[0].Content] = true
		}

		got := float64(carrying) / float64(cfg.Sessions)
		if math.Abs(got-fraction) > 0.05 {
			t.Errorf("fraction %v: %.3f of sessions carry a system prompt", fraction, got)
		}
		// Shared means shared: one prompt, byte-identical, or there is no
		// cross-session prefix for a policy to exploit.
		if carrying > 0 && len(prompts) != 1 {
			t.Errorf("fraction %v: %d distinct system prompts across %d sessions", fraction, len(prompts), carrying)
		}
		if carrying > 0 {
			for content := range prompts {
				if want := cfg.SystemPromptTokens * w.BytesPerToken(); len(content) != want {
					t.Errorf("fraction %v: the system prompt is %d bytes, wanted %d", fraction, len(content), want)
				}
			}
		}
	}
}

// Branching is the other cross-session prefix: sessions that share an ancestor
// hold its turns in common and diverge after it. Without it every shared prefix
// in the trace would be a whole session or a system prompt, and the prefix
// index would never see a partial match.
func TestAConfigurableFractionOfSessionsBranchFromACommonAncestor(t *testing.T) {
	cfg := multiTurnConfig()
	cfg.Sessions = 120
	cfg.BranchFraction = 1
	cfg.BranchFamilies = 4
	cfg.BranchTurns = 2
	w := newMultiTurn(t, cfg)

	families := map[string][]string{}
	for session, body := range observe(t, w, cfg) {
		families[body.Messages[0].Content] = append(families[body.Messages[0].Content], session)
	}
	if len(families) != cfg.BranchFamilies {
		t.Fatalf("%d sessions branching into %d families, wanted %d", cfg.Sessions, len(families), cfg.BranchFamilies)
	}

	// Inside a family the ancestor turns are byte-identical and the turn after
	// them is not, which is what makes it a branch rather than a duplicate.
	for _, sessions := range families {
		if len(sessions) < 2 {
			continue
		}
		a, b := sessionTurns(t, w, cfg, sessions[0]), sessionTurns(t, w, cfg, sessions[1])
		for k := range cfg.BranchTurns {
			if a[k] != b[k] {
				t.Errorf("two sessions of one family differ at ancestor turn %d", k)
			}
		}
		if a[cfg.BranchTurns] == b[cfg.BranchTurns] {
			t.Errorf("two sessions of one family are identical past the %d ancestor turns, so they never branched", cfg.BranchTurns)
		}
	}

	// With no branching every session opens on its own content.
	cfg.BranchFraction = 0
	unbranched := newMultiTurn(t, cfg)
	opens := map[string]bool{}
	for _, body := range observe(t, unbranched, cfg) {
		opens[body.Messages[0].Content] = true
	}
	if len(opens) != cfg.Sessions {
		t.Errorf("with branching off, %d sessions share only %d opening turns", cfg.Sessions, len(opens))
	}
}

// sessionTurns returns the user content of each turn of one session, found by
// walking to a user that draws it.
func sessionTurns(t *testing.T, w *bench.MultiTurn, cfg bench.MultiTurnWorkload, session string) []string {
	t.Helper()
	for user := range min(40*cfg.Sessions, bench.WorkloadStride) {
		if w.Next(user, 0).Session != session {
			continue
		}
		turns := make([]string, 0, cfg.TurnsPerSession)
		for k := range cfg.TurnsPerSession {
			body := decode(t, w.Next(user, k))
			turns = append(turns, body.Messages[len(body.Messages)-1].Content)
		}
		return turns
	}
	t.Fatalf("no user draws session %s", session)
	return nil
}

// ADR-0004: a workload deterministic in (user, turn) makes the second
// repetition of a cell re-send the first's prompts, which the replica answers
// out of its prefix cache instead of prefilling. The fixed workload is
// separated by shifting the user space; a multi-turn generator draws from a
// pool of sessions, so shifting the user alone would redraw the same sessions
// and re-send the same conversations. The shift has to reach the content.
func TestTwoCellsNeverResendEachOthersConversations(t *testing.T) {
	base := newMultiTurn(t, multiTurnConfig())

	bodies := func(offset int) map[string]bool {
		w := bench.Shifted(base, offset)
		seen := map[string]bool{}
		for user := range 8 {
			for turn := range 12 {
				seen[string(w.Next(user, turn).Body)] = true
			}
		}
		return seen
	}

	first := bodies(bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 1))
	for body := range bodies(bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 2)) {
		if first[body] {
			t.Fatal("a repetition re-sends the previous repetition's conversation, so it measures the prefix cache rather than prefill")
		}
	}

	// Within one cell the sharing is the point: turns of a session share their
	// history, so a later turn contains an earlier turn's bytes.
	w := bench.Shifted(base, bench.CellWorkloadOffset(bench.ClosedLoopAt(8), 1))
	opening := decode(t, w.Next(0, 0)).Messages[0].Content
	third := decode(t, w.Next(0, 2))
	if third.Messages[0].Content != opening {
		t.Error("a cell's own turns stopped sharing a history once the user space was shifted")
	}
}

// The knobs are the experiment: every one of them has to reach the bytes.
func TestSessionCountTurnsPromptAndOutputAreConfigurable(t *testing.T) {
	cfg := multiTurnConfig()
	cfg.Sessions = 9
	cfg.TurnsPerSession = 3
	cfg.PromptTokens = 100
	cfg.OutputTokens = 20
	w := newMultiTurn(t, cfg)

	sessions := map[string]bool{}
	for user := range 500 {
		sessions[w.Next(user, 0).Session] = true
	}
	if len(sessions) != cfg.Sessions {
		t.Errorf("the pool holds %d sessions, wanted %d", len(sessions), cfg.Sessions)
	}

	body := decode(t, w.Next(0, 0))
	if body.Model != testModel {
		t.Errorf("sent model %q, wanted %q", body.Model, testModel)
	}
	if body.MaxTokens != cfg.OutputTokens {
		t.Errorf("capped generation at %d tokens, wanted %d", body.MaxTokens, cfg.OutputTokens)
	}
	if !body.Stream {
		t.Error("the turn was not streamed, so there is no TTFT to measure")
	}
	if want := cfg.PromptTokens * w.BytesPerToken(); len(body.Messages[0].Content) != want {
		t.Errorf("a turn's prompt is %d bytes, wanted %d", len(body.Messages[0].Content), want)
	}
	if want := cfg.TurnsPerSession * (cfg.PromptTokens + cfg.OutputTokens); w.SessionTokens() != want {
		t.Errorf("a session is %d tokens, wanted %d", w.SessionTokens(), want)
	}
	// The turn after the last one starts a new visit, which is what bounds a
	// session at its configured length.
	if len(decode(t, w.Next(0, cfg.TurnsPerSession)).Messages) != 1 {
		t.Errorf("a session ran past its %d turns", cfg.TurnsPerSession)
	}
}

// A generator built on a contradictory configuration would produce a trace
// nobody could interpret, so it refuses rather than silently repairing itself.
func TestTheGeneratorRefusesAConfigurationThatCannotMeanAnything(t *testing.T) {
	cases := map[string]func(*bench.MultiTurnWorkload){
		"no model":              func(c *bench.MultiTurnWorkload) { c.Model = "" },
		"no sessions and no WS": func(c *bench.MultiTurnWorkload) { c.Sessions = 0 },
		"WS without a capacity": func(c *bench.MultiTurnWorkload) { c.Sessions, c.WorkingSet = 0, 1 },
		"a session count and a WS both": func(c *bench.MultiTurnWorkload) {
			c.WorkingSet, c.CapacityTokens = 1, measuredFleetCapacity
		},
		"negative skew":             func(c *bench.MultiTurnWorkload) { c.Skew = -1 },
		"fraction above one":        func(c *bench.MultiTurnWorkload) { c.SystemPromptFraction = 1.5 },
		"system prompt over budget": func(c *bench.MultiTurnWorkload) { c.SystemPromptFraction, c.SystemPromptTokens = 1, 448 },
		"ancestor is the whole session": func(c *bench.MultiTurnWorkload) {
			c.BranchFraction, c.BranchTurns = 1, 4
		},
		"a working set too small for a session": func(c *bench.MultiTurnWorkload) {
			c.Sessions, c.WorkingSet, c.CapacityTokens = 0, 0.001, 1000
		},
	}
	for name, break_ := range cases {
		cfg := multiTurnConfig()
		break_(&cfg)
		if w, err := bench.NewMultiTurn(cfg); err == nil {
			t.Errorf("%s: built a generator anyway (%s)", name, w.Name())
		}
	}
}

// The workload's name lands in every cell record, and a pressure grid whose
// cells cannot say which pressure they were run at is not a grid.
func TestTheNameCarriesThePressureTheCellWasRunAt(t *testing.T) {
	cfg := multiTurnConfig()
	cfg.Sessions = 0
	cfg.WorkingSet = 3
	cfg.CapacityTokens = measuredFleetCapacity
	cfg.Skew = 1.4
	w := newMultiTurn(t, cfg)

	for _, want := range []string{"multiturn", "ws=3", "skew=1.4", "sessions=1107", "turns=4"} {
		if !strings.Contains(w.Name(), want) {
			t.Errorf("the name %q does not say %q", w.Name(), want)
		}
	}
}

// Cell.Workload is the run's only record of what was sent, so two traces that
// offered different bytes must not render the same name. The knobs that shape
// sharing change the trace without changing its size, which is exactly the pair
// a size-only name would flatten together.
func TestTracesThatDifferInWhatTheySentAreNamedDifferently(t *testing.T) {
	names := map[string]string{}
	for label, tweak := range map[string]func(*bench.MultiTurnWorkload){
		"plain":            func(*bench.MultiTurnWorkload) {},
		"another seed":     func(c *bench.MultiTurnWorkload) { c.Seed++ },
		"a system prompt":  func(c *bench.MultiTurnWorkload) { c.SystemPromptFraction, c.SystemPromptTokens = 0.5, 128 },
		"a longer one":     func(c *bench.MultiTurnWorkload) { c.SystemPromptFraction, c.SystemPromptTokens = 0.5, 64 },
		"branching":        func(c *bench.MultiTurnWorkload) { c.BranchFraction, c.BranchFamilies, c.BranchTurns = 0.5, 4, 1 },
		"wider families":   func(c *bench.MultiTurnWorkload) { c.BranchFraction, c.BranchFamilies, c.BranchTurns = 0.5, 8, 1 },
		"deeper ancestors": func(c *bench.MultiTurnWorkload) { c.BranchFraction, c.BranchFamilies, c.BranchTurns = 0.5, 4, 2 },
		"other bytes":      func(c *bench.MultiTurnWorkload) { c.BytesPerToken = 3 },
	} {
		cfg := multiTurnConfig()
		tweak(&cfg)
		name := newMultiTurn(t, cfg).Name()
		if other, clash := names[name]; clash {
			t.Errorf("%q and %q both render as %s, so a cell cannot say which it ran", label, other, name)
		}
		names[name] = label
	}
}

// A pool sized for a WS point is only under that much memory pressure if the
// cell reaches all of it, and skew is what decides whether it does. The gap is
// reported rather than corrected for, so it has to be computable.
func TestSkewDiscountsHowMuchOfTheWorkingSetACellReaches(t *testing.T) {
	const visits = 5000

	reached := map[float64]float64{}
	for _, alpha := range []float64{0, 1.4} {
		cfg := multiTurnConfig()
		cfg.Sessions = 500
		cfg.Skew = alpha
		w := newMultiTurn(t, cfg)
		reached[alpha] = w.ExpectedDistinctSessions(visits)

		// The expectation has to describe the draws the generator actually
		// makes, or it is a second model of the workload rather than a
		// description of it.
		drawn := map[string]bool{}
		for user := range visits / 25 {
			for visit := range 25 {
				drawn[w.Next(user, visit*cfg.TurnsPerSession).Session] = true
			}
		}
		if got, want := float64(len(drawn)), reached[alpha]; math.Abs(got-want) > 0.1*want {
			t.Errorf("α=%v: %d sessions drawn against an expected %.1f", alpha, len(drawn), want)
		}
	}

	if reached[0] < 499 {
		t.Errorf("at α=0 a long cell reached only %.1f of 500 sessions", reached[0])
	}
	if reached[1.4] > 0.75*reached[0] {
		t.Errorf("at α=1.4 the cell still reached %.1f of 500 sessions, so skew is not concentrating the draws", reached[1.4])
	}
	if got := newMultiTurn(t, multiTurnConfig()).ExpectedDistinctSessions(0); got != 0 {
		t.Errorf("a cell of no visits reached %.1f sessions", got)
	}
}

// NaN passes every ordered comparison, so a range check written the obvious way
// admits it and the generator silently collapses onto one session.
func TestTheGeneratorRefusesAPressureThatIsNotANumber(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	cases := map[string]func(*bench.MultiTurnWorkload){
		"NaN skew":            func(c *bench.MultiTurnWorkload) { c.Skew = nan },
		"infinite skew":       func(c *bench.MultiTurnWorkload) { c.Skew = inf },
		"NaN system fraction": func(c *bench.MultiTurnWorkload) { c.SystemPromptFraction = nan },
		"NaN branch fraction": func(c *bench.MultiTurnWorkload) { c.BranchFraction = nan },
		"NaN working set": func(c *bench.MultiTurnWorkload) {
			c.Sessions, c.WorkingSet, c.CapacityTokens = 0, nan, measuredFleetCapacity
		},
		"infinite working set": func(c *bench.MultiTurnWorkload) {
			c.Sessions, c.WorkingSet, c.CapacityTokens = 0, inf, measuredFleetCapacity
		},
	}
	for name, tweak := range cases {
		cfg := multiTurnConfig()
		tweak(&cfg)
		if w, err := bench.NewMultiTurn(cfg); err == nil {
			t.Errorf("%s: built a generator anyway (%s)", name, w.Name())
		}
	}
}
