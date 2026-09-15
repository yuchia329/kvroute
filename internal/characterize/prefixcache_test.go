package characterize_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/characterize"
)

// The hit rate is derived rather than stored, so a record cannot carry a rate
// that disagrees with the counts it came from.
func TestHitRateFollowsFromTheCountsAndNotFromAStoredField(t *testing.T) {
	d := characterize.PrefixCacheDelta{Hits: 250, Queries: 1000, Read: true}
	if got := d.HitRate(); got != 0.25 {
		t.Errorf("hit rate = %v, want 0.25", got)
	}
	// Nothing read and nothing queried both yield zero, and neither may be
	// mistaken for a measured zero.
	if got := (characterize.PrefixCacheDelta{}).HitRate(); got != 0 {
		t.Errorf("unread hit rate = %v, want 0", got)
	}
	if got := (characterize.PrefixCacheDelta{Read: true}).HitRate(); got != 0 {
		t.Errorf("hit rate with no queries = %v, want 0", got)
	}
}

// Three different states that all look like "0%" and must not read the same:
// the counters were not read, they were read and never moved, and the cache
// genuinely answered almost nothing.
func TestTheThreeWaysAHitRateCanBeZeroAreToldApart(t *testing.T) {
	unread, _ := characterize.NewFloor(nil, 6, characterize.PrefixCacheDelta{}, 0).Usable()
	if unread {
		t.Error("a floor whose counters were never read reported itself usable")
	}

	idle := characterize.NewFloor(rows(60, 320*time.Millisecond, 8*time.Millisecond), 6,
		characterize.PrefixCacheDelta{Read: true}, 0)
	ok, why := idle.Usable()
	if ok {
		t.Error("a floor whose replica recorded no prefix-cache queries reported itself usable")
	}
	if !strings.Contains(why, "no prefix-cache queries") {
		t.Errorf("the reason was %q, which does not say the counters were read and did not move", why)
	}

	cold := characterize.NewFloor(rows(60, 320*time.Millisecond, 8*time.Millisecond), 6,
		characterize.PrefixCacheDelta{Hits: 120, Queries: 10000, Read: true}, 0)
	if ok, why := cold.Usable(); !ok {
		t.Errorf("a floor measured against a cold cache was refused: %s", why)
	}
}
