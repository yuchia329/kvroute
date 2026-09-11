package bench

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuchia329/kvroute/internal/policy"
)

// HashLeadingBlocks is the window the stateless prefix hash is run at, and the
// value every report of it has to state.
//
// Sixteen blocks is 1,024 bytes of prompt, which the workload's own geometry
// brackets from both sides. Below it: a session carrying the shared system
// prompt spends its first ~600 bytes on the chat envelope and on 128 declared
// tokens of prompt that 30% of sessions send identically, so a window shorter
// than ten blocks hashes those sessions to one key and concentrates the one kind
// of sharing idea.md §5 says to scatter. Above it: the window has to stay inside
// what every turn of a conversation resends unchanged, and a turn contributes
// 448 declared tokens — about 28 blocks — so a window past the first turn would
// give turn 2 a different key from turn 1 and the policy would lose the locality
// it exists for. Sixteen sits between the two with room either side.
//
// In the engine's own units it is roughly 644 tokens at the measured 1.59 prompt
// bytes per token, which is worth stating beside OpenAI's minimum cacheable
// prefix of 1,024 tokens: this window is shorter than the shortest prefix their
// cache will hold, and nothing here claims to have reproduced their number.
const HashLeadingBlocks = 16

// HashWeightGrid is the levels the weighting between the hash and the load term
// is measured at.
//
// The axis is in inflight requests per step down the hash's ranking, so it is
// the same unit the spill rule's load condition is swept in, and the same
// argument sizes it: a cache hit on this workload is worth a great deal of
// queueing — the concurrency-1 cells put a full prefill at roughly 285 ms of TTFT
// against a per-request queueing cost in the low tens of milliseconds — so the
// break-even is large and levels clustered near 1 would spend the run deflecting
// requests it should be keeping.
//
// The two ends are controls rather than settings. A weight of 0 is
// least-outstanding with a hash that decides nothing, and at the sweep's
// concurrency of 32 no replica can be more than 32 requests out of line, so 32
// is already the pure-hash end: the request stays on its hashed replica whatever
// the fleet looks like. Running both is what says whether the balance in between
// is doing anything at all, which is the one question this policy is run to
// answer.
var HashWeightGrid = []float64{0, 1, 4, 12, 32}

// ParseHash reads the hash grid point a command is given, written
// <leading-blocks>/<weight>.
//
// Here rather than in each command, for the reason ParseSpill is here: two
// parsers for one format are two places for a stray space or a negative to be
// accepted by one and rejected by the other.
func ParseHash(spec string) (policy.Hash, error) {
	blocks, weight, found := strings.Cut(strings.TrimSpace(spec), "/")
	if !found {
		return policy.Hash{}, fmt.Errorf("bench: a hash grid point is written as <leading-blocks>/<weight>, got %q", spec)
	}
	leading, err := strconv.Atoi(strings.TrimSpace(blocks))
	if err != nil {
		return policy.Hash{}, fmt.Errorf("bench: %q is not a count of leading prefix blocks: %w", blocks, err)
	}
	w, err := strconv.ParseFloat(strings.TrimSpace(weight), 64)
	if err != nil {
		return policy.Hash{}, fmt.Errorf("bench: %q is not a hash weight: %w", weight, err)
	}
	point := policy.Hash{LeadingBlocks: leading, HashWeight: w}
	if err := point.Validate(); err != nil {
		return policy.Hash{}, err
	}
	if !point.Stated() {
		// Zero is how a cell records that it ran a policy with no hash at all, so
		// a point deliberately written as zero would be indistinguishable from
		// every round-robin cell ever recorded.
		return policy.Hash{}, fmt.Errorf("bench: a window of %d blocks is not a point on the axis: it is how a cell records that its policy hashed nothing", point.LeadingBlocks)
	}
	return point, nil
}

// FormatHash renders a point into the spec the commands take, so a flag's
// default can be the package's own value rather than a second copy that drifts.
func FormatHash(p policy.Hash) string {
	return strconv.Itoa(p.LeadingBlocks) + "/" + strconv.FormatFloat(p.HashWeight, 'g', -1, 64)
}
