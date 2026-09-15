package characterize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RecordName is the file a run writes its record to, inside its own directory.
const RecordName = "characterization.json"

// Load reads a characterization record.
//
// It takes either the record or the directory holding it, because the two are what
// callers have: a command that ran the pass knows the directory it wrote to, and an
// operator quoting a path off a report has the file.
//
// It exists so that the figures a characterization establishes reach the runs that
// depend on them as the record rather than as retyped flags. The SLO is the sharp
// case: it is derived from a measured floor, and a threshold transcribed by hand is
// a threshold that can differ from the one that was derived, which would leave a
// goodput figure internally consistent and quietly wrong.
func Load(path string) (Characterization, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, RecordName)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Characterization{}, fmt.Errorf("characterize: %w", err)
	}
	var c Characterization
	if err := json.Unmarshal(contents, &c); err != nil {
		return Characterization{}, fmt.Errorf("characterize: %s is not a characterization record: %w", path, err)
	}
	return c, nil
}

// SLOUsable reports whether the SLO in this record can be applied to a sweep, and
// why not when it cannot.
//
// It asks about the SLO's own provenance and deliberately not about the record as a
// whole. An SLO derived from a flagged floor would be applied to every cell of
// every sweep with nothing in those cells recording that its derivation was in
// doubt, so the floor is checked — Floor.Usable is where that judgement already
// lives, covering the sample count, the prefix-cache guard and the floor rows' own
// flags.
//
// A characterization can carry flags that say nothing about the floor: the
// symmetry verdict is a claim about whether the replicas are interchangeable, and
// an under-warmed probe at the upper load level is about that probe. Refusing the
// SLO for those would block a sweep over a figure the SLO does not rest on. They
// still matter and they are still the caller's to report — Flagged and FlagReasons
// are there to be read, and Symmetry bears directly on whether a policy difference
// can be attributed to the policy — but they are a warning about the fleet, not a
// fault in the threshold.
func (c Characterization) SLOUsable() (bool, string) {
	if ok, why := c.Floor.Usable(); !ok {
		return false, "the floor the SLO is derived from cannot be built on: " + why
	}
	if c.SLO.TTFT <= 0 || c.SLO.ITL <= 0 {
		return false, "the record carries no derived SLO"
	}
	return true, ""
}
