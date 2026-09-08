package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
)

// MultiTurnWorkload configures the multi-turn generator.
//
// The zero value is not usable: a session count or a working set ratio has to
// come from somewhere, and the model is pinned engine configuration with no
// default. Everything else fills in.
type MultiTurnWorkload struct {
	// Model is what the replicas serve. No default, for the reason
	// FixedWorkload gives: ops/versions.env is its single source of truth.
	Model string

	// Sessions is the size of the session pool. Leave it zero and set
	// WorkingSet to derive it from the measured fleet capacity instead.
	Sessions int
	// WorkingSet is the WS point this trace offers: session tokens over
	// aggregate fleet KV. Set it with CapacityTokens, and Sessions follows.
	WorkingSet float64
	// CapacityTokens is the measured aggregate fleet KV that WorkingSet is a
	// ratio of. It is a measurement — 755,712 tokens read off all six replicas
	// — and not the by-hand estimate, which the fleet beat by 9.8%.
	CapacityTokens int

	// TurnsPerSession bounds a conversation. A virtual user that reaches the
	// end of one draws another.
	TurnsPerSession int
	// PromptTokens is the new user text each turn contributes, on top of the
	// history it resends.
	PromptTokens int
	// OutputTokens caps generation, and is also the size of the assistant reply
	// written back into the history.
	OutputTokens int

	// Skew is the Zipf exponent over the session pool: 0 is uniform, and
	// concentration rises from there. It is the axis that creates load
	// imbalance, which is the pressure session-sticky hashing has no answer
	// for, so it runs across its full range rather than sitting at a constant.
	Skew float64

	// SystemPromptFraction is how much of the pool carries the shared system
	// prompt: a prefix held in common by sessions that have nothing else in
	// common.
	SystemPromptFraction float64
	// SystemPromptTokens is how long that prompt is.
	SystemPromptTokens int

	// BranchFraction is how much of the pool descends from a common ancestor
	// rather than opening on its own content.
	BranchFraction float64
	// BranchFamilies is how many distinct ancestors those sessions share
	// between them.
	BranchFamilies int
	// BranchTurns is how many leading turns a branch holds in common with its
	// family before diverging. It must leave at least one turn to diverge in,
	// or two sessions of a family are one session.
	BranchTurns int

	// BytesPerToken converts the token budgets above into the bytes actually
	// sent.
	//
	// This is the arithmetic's one soft input, in the same sense as the KV
	// budget behind the capacity estimate: the harness runs no tokenizer, so
	// the conversion is declared rather than measured. Four bytes per token is
	// the figure the fake replica also assumes. It is recorded in the
	// workload's name so a table built on this trace says which conversion its
	// WS point was computed with, and it is a knob so a measured figure can
	// replace it without the WS arithmetic changing shape.
	BytesPerToken int

	// Seed makes the trace reproducible. The same seed sends the same bytes.
	Seed uint64
}

// MultiTurn is the parameterized multi-turn generator: the primary workload,
// synthetic on purpose.
//
// A fixed trace gives one point on a curve; the interesting result is a
// crossover, so the generator is what lets the pressure grid say where one
// policy overtakes another and why. It moves along two independent axes:
// working set ratio, which creates memory pressure and drives eviction, and
// Zipf skew, which creates load imbalance. Holding either fixed would sweep one
// pressure while leaving the other untested.
//
// It is deterministic in (user, turn) like every workload here, and draws from
// a pool of sessions rather than owning one per virtual user — which is what
// lets skew concentrate several users onto one conversation.
//
// The assistant's side of the history is synthesized rather than fed back from
// the replica. A generator that replayed real responses could not reproduce a
// trace, and reproducibility is what makes two policies comparable; the cost is
// that the history is the right shape and size but not the model's own words,
// which no part of the measurement depends on.
type MultiTurn struct {
	cfg      MultiTurnWorkload
	sessions int
	pool     *zipf
}

// Multi-turn generator defaults. The turn geometry multiplies out to a 2,048
// token session, which is the size idea.md §6 states the WS axis in and the
// divisor characterize.DefaultSessionTokens carries.
const (
	defaultTurnsPerSession = 4
	defaultPromptTokens    = 448
	defaultOutputTokens    = 64
	defaultSystemTokens    = 128
	defaultBranchFamilies  = 8
	defaultBranchTurns     = 1
	defaultBytesPerToken   = 4
)

// NewMultiTurn builds the generator, filling in defaults and refusing a
// configuration that cannot mean anything.
func NewMultiTurn(cfg MultiTurnWorkload) (*MultiTurn, error) {
	if cfg.TurnsPerSession <= 0 {
		cfg.TurnsPerSession = defaultTurnsPerSession
	}
	if cfg.PromptTokens <= 0 {
		cfg.PromptTokens = defaultPromptTokens
	}
	if cfg.OutputTokens <= 0 {
		cfg.OutputTokens = defaultOutputTokens
	}
	if cfg.SystemPromptTokens <= 0 {
		cfg.SystemPromptTokens = defaultSystemTokens
	}
	if cfg.BranchFamilies <= 0 {
		cfg.BranchFamilies = defaultBranchFamilies
	}
	if cfg.BranchTurns <= 0 {
		cfg.BranchTurns = defaultBranchTurns
	}
	if cfg.BytesPerToken <= 0 {
		cfg.BytesPerToken = defaultBytesPerToken
	}

	if cfg.Model == "" {
		return nil, errors.New("bench: the multi-turn generator needs the model the replicas serve; it is pinned in ops/versions.env")
	}
	// NaN fails every ordered comparison, so a bare `< 0` waves it through. An
	// all-NaN weight table collapses the whole pool onto one session, which
	// looks like a very skewed workload rather than like a broken one.
	if !finite(cfg.Skew) || cfg.Skew < 0 {
		return nil, fmt.Errorf("bench: Zipf skew is %v; it runs from 0, which is uniform, upward", cfg.Skew)
	}
	if err := checkFraction("SystemPromptFraction", cfg.SystemPromptFraction); err != nil {
		return nil, err
	}
	if err := checkFraction("BranchFraction", cfg.BranchFraction); err != nil {
		return nil, err
	}
	// The system prompt is spent out of a session's own token budget rather
	// than added to it, so it has to fit inside one turn's share.
	if cfg.SystemPromptFraction > 0 && cfg.SystemPromptTokens >= cfg.PromptTokens {
		return nil, fmt.Errorf("bench: a %d-token system prompt does not fit in a %d-token turn, so a session carrying one could not hold its working set flat",
			cfg.SystemPromptTokens, cfg.PromptTokens)
	}
	if cfg.BranchFraction > 0 && cfg.BranchTurns >= cfg.TurnsPerSession {
		return nil, fmt.Errorf("bench: %d ancestor turns out of %d leaves nothing for a branch to diverge in, so two sessions of a family would be one session",
			cfg.BranchTurns, cfg.TurnsPerSession)
	}

	sessionTokens := cfg.TurnsPerSession * (cfg.PromptTokens + cfg.OutputTokens)
	sessions, err := poolSize(cfg, sessionTokens)
	if err != nil {
		return nil, err
	}

	return &MultiTurn{cfg: cfg, sessions: sessions, pool: newZipf(sessions, cfg.Skew)}, nil
}

// poolSize settles how many sessions the trace offers: either the stated count,
// or the count a WS ratio calls for against the measured capacity.
//
// The two are alternatives rather than a default and an override. A generator
// told both would have to pick one and silently discard the other, and the one
// it discarded would still be printed in the name.
func poolSize(cfg MultiTurnWorkload, sessionTokens int) (int, error) {
	switch {
	case cfg.Sessions > 0 && cfg.WorkingSet > 0:
		return 0, fmt.Errorf("bench: given both %d sessions and a working set ratio of %v; the ratio derives the session count, so pass one or the other",
			cfg.Sessions, cfg.WorkingSet)
	case cfg.Sessions > 0:
		return cfg.Sessions, nil
	case !finite(cfg.WorkingSet):
		return 0, fmt.Errorf("bench: the working set ratio is %v, which is not a ratio", cfg.WorkingSet)
	case cfg.WorkingSet <= 0:
		return 0, errors.New("bench: the multi-turn generator needs a session count, or a working set ratio and the measured fleet capacity to derive one from")
	case cfg.CapacityTokens <= 0:
		// Deriving WS off anything but the measurement is the mistake the
		// characterization gates exist to prevent: the estimate was 9.8% low,
		// and every point of the pressure grid moves with the denominator.
		return 0, fmt.Errorf("bench: a working set ratio of %v needs the measured aggregate fleet KV to be a ratio of", cfg.WorkingSet)
	}

	// The same arithmetic as characterize.Capacity.WorkingSets, which is where
	// the ratios are published: offered tokens over capacity, divided into
	// whole sessions.
	sessions := int(cfg.WorkingSet*float64(cfg.CapacityTokens)) / sessionTokens
	if sessions <= 0 {
		return 0, fmt.Errorf("bench: a working set ratio of %v against %d tokens of capacity does not reach one %d-token session",
			cfg.WorkingSet, cfg.CapacityTokens, sessionTokens)
	}
	return sessions, nil
}

// finite rejects the two values that pass every ordered comparison without
// meaning anything: NaN, which is neither inside a range nor outside it, and an
// infinity, which converts to an undefined integer.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func checkFraction(name string, value float64) error {
	if !finite(value) || value < 0 || value > 1 {
		return fmt.Errorf("bench: %s is %v; it is a fraction of the session pool, so it runs from 0 to 1", name, value)
	}
	return nil
}

// Sessions is the size of the session pool this trace draws from.
func (m *MultiTurn) Sessions() int { return m.sessions }

// SessionTokens is one session's KV footprint: every turn's prompt and every
// turn's generation, which is what the last turn of the conversation has
// resident behind it.
//
// It is the same for every session whatever the sharing knobs are set to. A
// session carrying the shared system prompt spends part of its own budget on it
// rather than adding to it, and a branch holds its ancestor's turns in place of
// its own rather than on top of them. That keeps the two pressure axes
// independent: turning up sharing changes how much of the working set is held
// in common, not how large it is. What sharing does change is how much of it
// has to be *resident at once*, which is the effect the experiment measures.
func (m *MultiTurn) SessionTokens() int {
	return m.cfg.TurnsPerSession * (m.cfg.PromptTokens + m.cfg.OutputTokens)
}

// OfferedTokens is the pool's whole working set: the token volume the WS ratio
// names.
//
// It is the nominal figure — what the trace offers if every session is drawn.
// Skew decides how much of that a cell of finite length actually reaches, and
// at the top of the skew axis that is a large discount: a pool sized for WS 8
// is not under WS 8 of memory pressure if α concentrates the draws onto a
// tenth of it. The two pressures stay separately dialable, but they are not
// separately *realised*, so the pressure grid has to report what a cell reached
// as well as what it asked for. ExpectedDistinctSessions is that figure.
func (m *MultiTurn) OfferedTokens() int { return m.sessions * m.SessionTokens() }

// ExpectedDistinctSessions is how much of the pool a cell of the given number
// of session visits touches — one visit being one virtual user's run through
// one conversation, so a cell of N requests makes N/TurnsPerSession of them.
//
// This is what keeps the WS label honest under skew. At α=0 it climbs to the
// whole pool; at α=1.4 it saturates well short of it, and the difference is the
// gap between the memory pressure a cell was configured for and the pressure it
// applied. Reported rather than corrected for: rescaling the pool to hit a
// realised WS would make the session count depend on cell duration, and a WS
// point that moves when a cell gets longer is not a point.
func (m *MultiTurn) ExpectedDistinctSessions(visits int) float64 {
	if visits <= 0 {
		return 0
	}
	// Standard occupancy: a session is missed by every one of the draws, or it
	// is touched.
	expected := 0.0
	previous := 0.0
	for _, cumulative := range m.pool.cdf {
		p := cumulative - previous
		previous = cumulative
		expected += 1 - math.Pow(1-p, float64(visits))
	}
	return expected
}

// BytesPerToken is the declared conversion between the token budgets the WS
// axis is stated in and the bytes on the wire.
func (m *MultiTurn) BytesPerToken() int { return m.cfg.BytesPerToken }

// Name identifies the workload in the cell record. A pressure grid whose cells
// cannot say which pressure produced them is not a grid, so both axes are in
// here alongside the geometry.
//
// Every knob that changes the bytes is in it, including the ones that only
// shape the sharing. Cell.Workload is the run's single record of what was sent,
// so two traces that differ in what they offered must not render the same
// string — a grid cannot go back and ask the generator afterwards.
func (m *MultiTurn) Name() string {
	var b strings.Builder
	fmt.Fprintf(&b, "multiturn(sessions=%d", m.sessions)
	if m.cfg.WorkingSet > 0 {
		fmt.Fprintf(&b, ",ws=%g", m.cfg.WorkingSet)
	}
	fmt.Fprintf(&b, ",skew=%g,turns=%d,prompt=%dt,output=%dt,system=%gx%dt,branch=%gx%dfam%dt,bpt=%d,seed=%d)",
		m.cfg.Skew, m.cfg.TurnsPerSession, m.cfg.PromptTokens, m.cfg.OutputTokens,
		m.cfg.SystemPromptFraction, m.cfg.SystemPromptTokens,
		m.cfg.BranchFraction, m.cfg.BranchFamilies, m.cfg.BranchTurns,
		m.cfg.BytesPerToken, m.cfg.Seed)
	return b.String()
}

// Next builds the turn a virtual user should send next.
//
// The user's turn counter runs forever, so it is cut into visits: a run of
// TurnsPerSession turns against one session, then a fresh draw. Two users can
// hold the same session at once, and under skew they routinely will — that is
// what a hot conversation is, and it is the load imbalance the skew axis exists
// to create.
func (m *MultiTurn) Next(user, turn int) Turn {
	// The user space above one stride belongs to another cell (see Shifted).
	// The epoch reaches the content, not just the draw: a generator that varied
	// only which sessions were drawn would have the next cell redraw the same
	// pool and re-send the same conversations, which is the prefix-cache
	// contamination ADR-0004 records — the shift has to change the bytes.
	//
	// The draw itself is deliberately epoch-free, so every cell offers the same
	// shape of load and differs only in what that load says.
	epoch, local := user/WorkloadStride, user%WorkloadStride
	visit, index := turn/m.cfg.TurnsPerSession, turn%m.cfg.TurnsPerSession
	session := m.draw(local, visit)

	messages := make([]map[string]string, 0, 2*index+2)
	if m.carriesSystemPrompt(session) {
		messages = append(messages, message("system", m.systemPrompt(epoch)))
	}
	// Every prior turn is resent in full, which is what gives a session a
	// growing shared prefix for a policy to preserve.
	for prior := range index {
		messages = append(messages,
			message("user", m.userContent(epoch, session, prior)),
			message("assistant", m.assistantContent(epoch, session, prior)))
	}
	messages = append(messages, message("user", m.userContent(epoch, session, index)))

	body, err := json.Marshal(map[string]any{
		"model":      m.cfg.Model,
		"messages":   messages,
		"stream":     true,
		"max_tokens": m.cfg.OutputTokens,
	})
	if err != nil {
		// Strings and ints built here; there is no input that can make it
		// unmarshalable.
		panic("bench: multi-turn generator produced an unmarshalable body: " + err.Error())
	}
	return Turn{Session: fmt.Sprintf("sess-%d-%d", epoch, session), Body: body}
}

func message(role, content string) map[string]string {
	return map[string]string{"role": role, "content": content}
}

// draw picks the session a virtual user's visit is spent on.
func (m *MultiTurn) draw(user, visit int) int {
	rng := rand.New(rand.NewPCG(mix(m.cfg.Seed, tagDraw, uint64(user)), uint64(visit)+1))
	return m.pool.sample(rng.Float64())
}

// userContent is the new text one turn contributes.
//
// The session's last turn is where a shared system prompt is paid for, rather
// than the first: turns before it may be an ancestor's, held byte-identical
// across a family, and shortening one of those would break the family apart
// depending on which of its members happened to carry a system prompt.
func (m *MultiTurn) userContent(epoch, session, turn int) string {
	tokens := m.cfg.PromptTokens
	if turn == m.cfg.TurnsPerSession-1 && m.carriesSystemPrompt(session) {
		tokens -= m.cfg.SystemPromptTokens
	}
	return filler(m.contentSeed(epoch, session, turn, roleUser), tokens, m.cfg.BytesPerToken)
}

// assistantContent is the reply written back into the history. It is the size
// generation was capped at, so the history grows by what a real exchange would
// have added to it.
func (m *MultiTurn) assistantContent(epoch, session, turn int) string {
	return filler(m.contentSeed(epoch, session, turn, roleAssistant), m.cfg.OutputTokens, m.cfg.BytesPerToken)
}

// contentSeed decides whose content a turn is. A branch's leading turns belong
// to its family, so every session descending from that ancestor sends the same
// bytes for them and the prefix index sees a partial match rather than a whole
// session or nothing.
func (m *MultiTurn) contentSeed(epoch, session, turn int, role uint64) uint64 {
	if turn < m.cfg.BranchTurns && m.branches(session) {
		return mix(m.cfg.Seed, tagAncestor, uint64(epoch), uint64(m.family(session)), uint64(turn), role)
	}
	return mix(m.cfg.Seed, tagTurn, uint64(epoch), uint64(session), uint64(turn), role)
}

// systemPrompt is shared by every session that carries one, which makes it the
// one prefix held in common across otherwise unrelated conversations.
func (m *MultiTurn) systemPrompt(epoch int) string {
	return filler(mix(m.cfg.Seed, tagSystemPrompt, uint64(epoch)), m.cfg.SystemPromptTokens, m.cfg.BytesPerToken)
}

// carriesSystemPrompt, branches and family are decided on the session alone and
// never on the epoch, so two cells offer the same structure of sharing and
// differ only in the bytes that structure is made of.
func (m *MultiTurn) carriesSystemPrompt(session int) bool {
	return holds(mix(m.cfg.Seed, tagSystemPick, uint64(session)), m.cfg.SystemPromptFraction)
}

func (m *MultiTurn) branches(session int) bool {
	return holds(mix(m.cfg.Seed, tagBranchPick, uint64(session)), m.cfg.BranchFraction)
}

func (m *MultiTurn) family(session int) int {
	return int(mix(m.cfg.Seed, tagFamily, uint64(session)) % uint64(m.cfg.BranchFamilies))
}

// Tags keep the hashes above in separate spaces, so the coin that decides
// whether a session carries a system prompt is not also the coin that decides
// whether it branches.
const (
	tagDraw uint64 = iota + 1
	tagTurn
	tagAncestor
	tagSystemPrompt
	tagSystemPick
	tagBranchPick
	tagFamily
	roleUser
	roleAssistant
)

// fractionScale is the granularity a fraction of the pool is decided at. A
// million buckets is far finer than any pool the pressure grid reaches, so the
// realised fraction is the stated one to well inside the tolerance a test could
// assert.
const fractionScale = 1 << 20

func holds(h uint64, fraction float64) bool {
	switch {
	case fraction <= 0:
		return false
	case fraction >= 1:
		return true
	}
	return h%fractionScale < uint64(fraction*fractionScale)
}

// filler builds exactly tokens*bytesPerToken bytes of content, deterministic in
// its seed. Hex words, so no two pieces of content share a prefix by accident
// and the length is exact after truncation.
func filler(seed uint64, tokens, bytesPerToken int) string {
	n := tokens * bytesPerToken
	if n <= 0 {
		return ""
	}
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	var b strings.Builder
	b.Grow(n + 9)
	for b.Len() < n {
		fmt.Fprintf(&b, "%08x ", rng.Uint32())
	}
	return b.String()[:n]
}

// mix folds its arguments into one seed. Deterministic across runs and
// platforms, which a map iteration or a pointer would not be.
func mix(values ...uint64) uint64 {
	h := uint64(0x9e3779b97f4a7c15)
	for _, v := range values {
		h ^= v
		h *= 0xff51afd7ed558ccd
		h ^= h >> 33
		h *= 0xc4ceb9fe1a85ec53
		h ^= h >> 29
	}
	return h
}

// zipf samples a finite pool with weights proportional to 1/rank^alpha.
//
// The standard library's Zipf sampler requires an exponent above one, which
// would put two of the three points on the skew axis — uniform and α=1.0 — out
// of reach. The axis runs from uniform upward by design, so the distribution is
// built here as a table instead: the pool is small enough that an exact
// cumulative distribution costs nothing and is easier to be sure of than a
// rejection sampler.
type zipf struct {
	cdf []float64
}

func newZipf(n int, alpha float64) *zipf {
	cdf := make([]float64, n)
	total := 0.0
	for i := range n {
		total += math.Pow(float64(i+1), -alpha)
		cdf[i] = total
	}
	for i := range cdf {
		cdf[i] /= total
	}
	return &zipf{cdf: cdf}
}

// sample maps a uniform draw in [0,1) onto the pool.
func (z *zipf) sample(u float64) int {
	// The last entry is 1 up to rounding, so a draw just under one can land
	// past the end.
	return min(sort.SearchFloat64s(z.cdf, u), len(z.cdf)-1)
}
