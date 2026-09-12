package router_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
)

// A prompt long enough that the index has leading blocks to believe in, so the
// router claims something and the claim can be honoured or not.
func scoringRequest(stream bool) string {
	content := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 40)
	body := `{"model":"m","messages":[{"role":"system","content":"` + content +
		`"},{"role":"user","content":"hello"}],"stream":` + map[bool]string{true: "true", false: "false"}[stream]
	if stream {
		body += `,"stream_options":{"include_usage":true}`
	}
	return body + "}"
}

// affinityRouter runs prefix affinity in front of one fake replica that reports
// serving the given share of every prompt out of its cache.
func affinityRouter(t *testing.T, cached float64) routed {
	t.Helper()
	_, base := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 4, CachedPromptFraction: cached})
	index, err := prefix.New(prefix.Config{NodeCap: 4096, TTL: time.Minute})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	return startRouterWith(t, policy.NewPrefixAffinity(index, policy.Spill{}), "replica-0="+base)
}

// honouredOf pulls the replica's rate off the fleet the router is deciding
// from, which is where the spill rule reads it.
func honouredOf(t *testing.T, rt routed, id string) (float64, bool, int) {
	t.Helper()
	c, present := rt.fleet.State().Candidate(id)
	if !present {
		t.Fatalf("no replica %s in the snapshot", id)
	}
	return c.Honoured.Fraction, c.Honoured.Read, c.Honoured.Claims
}

// The whole loop, end to end: the router claims a replica holds part of a
// prompt, the replica answers and says in its usage block how much of it was
// really cached, and the router's belief about that replica moves as a result.
//
// This is the signal ADR-0011 replaced the KV gauge with, and it is the only
// test that exercises it through a real response rather than through a value
// handed to the fleet.
func TestAReplicaHonouringItsClaimsIsBelievedThroughTheResponsesItSends(t *testing.T) {
	rt := affinityRouter(t, 1.0)

	// One turn is not evidence: the window's quorum is above it.
	post(t, rt.url, scoringRequest(true))
	if _, read, _ := honouredOf(t, rt, "replica-0"); read {
		t.Fatal("a rate was read off a single request")
	}

	// The first turn had nothing in the index to claim, so it scored nothing;
	// every turn after it claims the blocks the first admitted.
	for range 16 {
		post(t, rt.url, scoringRequest(true))
	}

	fraction, read, claims := honouredOf(t, rt, "replica-0")
	if !read {
		t.Fatalf("seventeen answered requests left the rate unread (%d claims)", claims)
	}
	if fraction != 1 {
		t.Errorf("rate = %v over %d claims, want 1: the replica reported serving every prompt token from cache",
			fraction, claims)
	}
}

// And the direction that matters: a replica that has evicted what the index
// believes it holds reports it, and the rate falls to where a low-water mark
// can see it.
func TestAReplicaThatHasEvictedWhatWasClaimedStopsBeingBelieved(t *testing.T) {
	rt := affinityRouter(t, 0)

	for range 17 {
		post(t, rt.url, scoringRequest(true))
	}

	fraction, read, claims := honouredOf(t, rt, "replica-0")
	if !read {
		t.Fatalf("seventeen answered requests left the rate unread (%d claims)", claims)
	}
	if fraction != 0 {
		t.Errorf("rate = %v over %d claims, want 0: the replica reported prefilling every prompt token",
			fraction, claims)
	}
}

// The blocking shape carries its usage block too, and a run that measured it
// would otherwise have a residency signal that existed only under streaming.
func TestABlockingAnswerFeedsTheRateAsAStreamedOneDoes(t *testing.T) {
	rt := affinityRouter(t, 1.0)

	for range 17 {
		post(t, rt.url, scoringRequest(false))
	}

	if _, read, claims := honouredOf(t, rt, "replica-0"); !read {
		t.Errorf("seventeen blocking answers left the rate unread (%d claims)", claims)
	}
}

// A replica started without --enable-prompt-tokens-details answers with usage
// and no breakdown. Scoring that as a breakdown of zero would read every such
// replica as evicting everything, and a fleet-wide missing engine flag would
// present as a fleet-wide eviction storm.
func TestAReplicaThatReportsNoCachedBreakdownIsNotScored(t *testing.T) {
	_, base := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 4,
		CachedPromptFraction: 1.0, OmitPromptTokensDetails: true})
	index, err := prefix.New(prefix.Config{NodeCap: 4096, TTL: time.Minute})
	if err != nil {
		t.Fatalf("prefix.New: %v", err)
	}
	rt := startRouterWith(t, policy.NewPrefixAffinity(index, policy.Spill{}), "replica-0="+base)

	for range 17 {
		post(t, rt.url, scoringRequest(true))
	}

	if fraction, read, claims := honouredOf(t, rt, "replica-0"); read {
		t.Errorf("a replica publishing no cached-token breakdown was scored at %v over %d claims", fraction, claims)
	}
}

// A policy that consults no index claims nothing, so it feeds the rate nothing.
// Scoring its requests as claims perfectly honoured would drag every replica's
// rate to one and silently disable the condition for any policy that ran after
// it.
func TestAPolicyThatClaimsNothingFeedsTheRateNothing(t *testing.T) {
	_, base := startFake(t, fakereplica.Config{ID: "replica-0", OutputTokens: 4, CachedPromptFraction: 1.0})
	rt := startRouterWith(t, policy.NewRoundRobin(), "replica-0="+base)

	for range 17 {
		post(t, rt.url, scoringRequest(true))
	}

	if fraction, read, claims := honouredOf(t, rt, "replica-0"); read {
		t.Errorf("round robin scored the replica at %v over %d claims, having claimed nothing", fraction, claims)
	}
}

// The rate the decision was made against is on the row, which is where the
// grid's levels are cut from and where the correlation against inflight is
// measured.
func TestTheRowCarriesTheRateTheDecisionWasMadeAgainst(t *testing.T) {
	rt := affinityRouter(t, 1.0)

	for range 17 {
		post(t, rt.url, scoringRequest(true))
	}
	rows := rt.rows.wait(t, 17)

	last := rows[len(rows)-1]
	if !last.HonouredRead {
		t.Fatalf("the last row carries no rate after %d answered requests", len(rows))
	}
	if last.HonouredRate != 1 || last.HonouredClaims < 8 {
		t.Errorf("row carries rate %v over %d claims, want 1 over at least a quorum",
			last.HonouredRate, last.HonouredClaims)
	}
	if last.PromptBytes <= 0 {
		t.Error("the row carries no prompt length, so its byte-denominated match cannot be converted")
	}
}
