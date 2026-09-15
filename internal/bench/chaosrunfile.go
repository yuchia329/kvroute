package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ChaosRunFile is the name a chaos run's record is written under, in the run's
// own directory beside its rows and its report.
const ChaosRunFile = "run.json"

// SaveChaosRun writes a chaos run's record into dir.
//
// Written through a temporary name and renamed into place, as a cell record is,
// so that its presence means the run finished: a run cut off partway leaves its
// rows and no run record, and nothing will read it as a result.
func SaveChaosRun(dir string, s ChaosRun) error {
	contents, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: marshal chaos run %s: %w", s.ID, err)
	}
	path := filepath.Join(dir, ChaosRunFile)
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("bench: write chaos run %s: %w", s.ID, err)
	}
	return os.Rename(tmp, path)
}

// LoadChaosRun reads the record a finished chaos run left in dir.
func LoadChaosRun(dir string) (ChaosRun, error) {
	path := filepath.Join(dir, ChaosRunFile)
	contents, err := os.ReadFile(path)
	if err != nil {
		return ChaosRun{}, fmt.Errorf("bench: %s holds no finished chaos run: %w", dir, err)
	}
	var s ChaosRun
	if err := json.Unmarshal(contents, &s); err != nil {
		return ChaosRun{}, fmt.Errorf("bench: %s is not a chaos run's record: %w", path, err)
	}
	return s, nil
}
