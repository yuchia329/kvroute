// Package box_test asserts that the Makefile's run targets work in both places
// they are run from.
//
// A workstation has a Go toolchain and a checkout, so `make bench` builds
// bin/bench and runs it. The fleet host has neither: ~/kvroute there is scp'd
// files rather than a clone, and the only binaries on it are the
// bin/*-linux-amd64 that `make linux` cross-compiled. Every run target used to
// depend on `build` and name the unsuffixed binary, so none of them could run
// where the fleet is, and every run on the box was driven by a hand-written
// script instead (#32).
//
// These assertions read `make -n`, which expands a recipe without running it, so
// they need neither a fleet nor a Go build.
package box_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repo root, two levels up from this package.
const root = "../.."

// runTargets are the targets that invoke one of our binaries against the fleet,
// paired with the command each one runs. Every one of them has to work on the
// box, because every one of them is a measurement.
var runTargets = map[string]string{
	"bench":              "bench",
	"goodput":            "bench",
	"pressure-grid":      "bench",
	"tunables-residency": "bench",
	"tunables-load":      "bench",
	"hash-weight":        "bench",
	"chaos":              "chaos",
	"characterize":       "characterize",
	"contention":         "characterize",
	"run-router":         "router",
	"run-fake":           "fakereplica",
	"calibrate":          "calibrate",
}

// reportTargets read recorded rows and need no fleet, but they are still run on
// the box at the end of a sweep, so they name a binary the same way.
var reportTargets = map[string]string{
	"compare":      "compare",
	"divergence":   "divergence",
	"pressure-map": "pressuremap",
	"recovery":     "recovery",
	"spill-signal": "spillsignal",
	"disagg":       "disagg",
}

func makeDryRun(t *testing.T, target string, vars ...string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make on PATH")
	}
	args := append([]string{"-n", target}, vars...)
	cmd := exec.Command("make", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s %v: %v\n%s", target, vars, err, out)
	}
	return string(out)
}

// TestBoxTargetsRunTheCrossCompiledBinaries is the issue's first criterion: the
// targets run on the fleet host, against what `make linux` produced.
func TestBoxTargetsRunTheCrossCompiledBinaries(t *testing.T) {
	for target, cmd := range allTargets() {
		t.Run(target, func(t *testing.T) {
			out := makeDryRun(t, target, "BOX=1")
			want := "bin/" + cmd + "-linux-amd64 "
			if !strings.Contains(out, want) {
				t.Errorf("make -n %s BOX=1 does not run %s:\n%s", target, want, out)
			}
			if strings.Contains(out, "bin/"+cmd+" ") {
				t.Errorf("make -n %s BOX=1 runs the unsuffixed bin/%s, which does not exist on the box:\n%s", target, cmd, out)
			}
		})
	}
}

// TestWorkstationTargetsRunThisMachinesBinaries is the second criterion: the
// same targets still run on a workstation, unchanged.
func TestWorkstationTargetsRunThisMachinesBinaries(t *testing.T) {
	for target, cmd := range allTargets() {
		t.Run(target, func(t *testing.T) {
			out := makeDryRun(t, target, "BOX=0")
			if !strings.Contains(out, "bin/"+cmd+" ") {
				t.Errorf("make -n %s BOX=0 does not run bin/%s:\n%s", target, cmd, out)
			}
			if strings.Contains(out, "-linux-amd64") {
				t.Errorf("make -n %s BOX=0 runs a cross-compiled binary:\n%s", target, out)
			}
		})
	}
}

func allTargets() map[string]string {
	all := map[string]string{}
	for k, v := range runTargets {
		all[k] = v
	}
	for k, v := range reportTargets {
		all[k] = v
	}
	return all
}

// TestBoxBuildStepsAside: `build` is a prerequisite of every run target, so on a
// host with no Go toolchain it has to step aside rather than fail.
func TestBoxBuildStepsAside(t *testing.T) {
	out := makeDryRun(t, "build", "BOX=1")
	if strings.Contains(out, "go build") {
		t.Errorf("make -n build BOX=1 still compiles, and the box has no Go toolchain:\n%s", out)
	}
}

func TestWorkstationBuildStillBuilds(t *testing.T) {
	out := makeDryRun(t, "build", "BOX=0")
	if !strings.Contains(out, "go build") {
		t.Errorf("make -n build BOX=0 does not compile:\n%s", out)
	}
}

// TestBoxIsDetectedFromTheToolchain: nothing has to be passed on the box. Its
// mark is that it cannot build, so the switch is `command -v go` rather than the
// operating system — a Linux workstation with Go behaves like this Mac.
func TestBoxIsDetectedFromTheToolchain(t *testing.T) {
	out := makeDryRun(t, "bench", "GO=definitely-not-a-go-toolchain")
	if !strings.Contains(out, "bin/bench-linux-amd64 ") {
		t.Errorf("make -n bench with no Go toolchain on PATH does not run the cross-compiled bench:\n%s", out)
	}
}

// TestLinuxCrossCompilesEveryCommand: a target that names bin/x-linux-amd64 and
// finds nothing there is the same failure #32 was. The list `make linux` carried
// had already fallen four commands behind cmd/, so it is read from cmd/ instead.
func TestLinuxCrossCompilesEveryCommand(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	// Named as a whole word rather than as a path, so the assertion holds whether
	// the recipe lists the commands or loops over them.
	out := makeDryRun(t, "linux")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(e.Name()) + `\b`).MatchString(out) {
			t.Errorf("make linux does not cross-compile ./cmd/%s, so no box target can run it:\n%s", e.Name(), out)
		}
	}
}
