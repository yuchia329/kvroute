// Command preflight refuses to let the fleet start on dirty GPUs.
//
// The box is shared. A replica that starts beside someone else's job — or
// beside a leftover of your own, which has already been the actual problem once
// — measures their workload as well as its own, and nothing downstream can tell
// afterwards that it did. So the check is on the way in, and it exits non-zero
// rather than warning.
//
//	preflight -gpus 6 -threshold-mib 256
//
// It is deliberately not exempting our own processes: at preflight time there
// is no fleet yet, so every process on a card is contamination whoever started
// it. Exempting the fleet's own processes is the *other* job the same probe
// does, during a cell, where a running fleet is expected.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yuchia329/kvroute/internal/gpu"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "preflight: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		gpus      = flag.Int("gpus", 6, "how many GPUs the fleet uses, checked as indexes 0..n-1")
		threshold = flag.Int("threshold-mib", 256, "a GPU holding at least this much memory is dirty")
		timeout   = flag.Duration("timeout", 30*time.Second, "how long to wait for nvidia-smi")
		asJSON    = flag.Bool("json", false, "print the snapshot as JSON instead of a report")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	indexes := make([]int, 0, *gpus)
	for i := range *gpus {
		indexes = append(indexes, i)
	}
	snapshot, err := gpu.New().Snapshot(ctx)
	if err != nil {
		return err
	}
	fleet := snapshot.Limit(indexes)
	if len(fleet.Devices) != *gpus {
		return fmt.Errorf("found %d of the %d expected GPUs; the host is not the one this fleet is configured for", len(fleet.Devices), *gpus)
	}

	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(fleet); err != nil {
			return err
		}
	}

	dirty := fleet.Dirty(*threshold)
	if len(dirty) == 0 {
		if !*asJSON {
			fmt.Printf("preflight: %d GPUs clean, all below %d MiB\n", len(fleet.Devices), *threshold)
		}
		return nil
	}

	// Everything the operator needs to decide whether to wait or to kill
	// something, on the refusal itself: which cards, how much, and who.
	fmt.Fprintf(os.Stderr, "preflight: %d of %d GPUs already hold memory (threshold %d MiB)\n", len(dirty), len(fleet.Devices), *threshold)
	for _, d := range dirty {
		fmt.Fprintf(os.Stderr, "  GPU %d: %d MiB used, %d%% utilization\n", d.Index, d.MemoryUsedMiB, d.UtilizationPct)
	}
	// Nothing is exempt here, so every process on the fleet's cards is listed.
	held := fleet.Foreign(nil)
	if len(held) == 0 {
		fmt.Fprintln(os.Stderr, "  no process is attributable: the memory belongs to a process nvidia-smi cannot see, most likely another user's")
	}
	for _, p := range held {
		fmt.Fprintf(os.Stderr, "  %s\n", p)
	}
	return fmt.Errorf("refusing to start on dirty GPUs")
}
