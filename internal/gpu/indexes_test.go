package gpu_test

import (
	"slices"
	"testing"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// The fleet is not the first n cards. GPU 3 throttles thermally under
// simultaneous load, so the policy comparison runs on 0,1,2,4,5 — a set no count
// can name, and the reason this takes a list at all.
func TestAFleetThatSkipsACardCanBeNamed(t *testing.T) {
	got, err := gpu.ParseIndexes("0,1,2,4,5")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := []int{0, 1, 2, 4, 5}; !slices.Equal(got, want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
	if round := gpu.FormatIndexes(got); round != "0,1,2,4,5" {
		t.Errorf("formatted back as %q", round)
	}
}

// versions.env holds a shell list and the flags take a comma-separated one.
// Requiring the operator to convert between them is a step that gets skipped.
func TestASpaceSeparatedShellListIsAcceptedToo(t *testing.T) {
	got, err := gpu.ParseIndexes(" 0 1 2 4 5 ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := []int{0, 1, 2, 4, 5}; !slices.Equal(got, want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
}

// Order is the caller's, not sorted for them: the list names a fleet, and a
// reader comparing it against ops/versions.env should see the same thing.
func TestTheListKeepsTheOrderItWasGiven(t *testing.T) {
	got, _ := gpu.ParseIndexes("5,4,0")
	if want := []int{5, 4, 0}; !slices.Equal(got, want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
}

// Every rejection here is a fleet that would have been sampled wrong, which is
// silent: the sampler would watch a card the fleet does not own and miss one it
// does, and a foreign process on the unwatched card would never be seen.
func TestAnUnusableListIsRefusedRatherThanGuessedAt(t *testing.T) {
	for _, spec := range []string{"", "   ", "0,1,x", "0,-1", "0,1,1"} {
		if got, err := gpu.ParseIndexes(spec); err == nil {
			t.Errorf("%q parsed as %v, want an error", spec, got)
		}
	}
}
