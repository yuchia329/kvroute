// Command calibrate measures the two bounds the prefix index runs with, and
// writes them to a file the router is started against.
//
//	calibrate -replicas "$(ops/fleet.sh replicas)" \
//	          -from runs/concurrency \
//	          -out runs/prefix-calibration.json
//
// The index's node cap and TTL are modelling decisions about hardware the router
// does not own, and idea.md §4.3 is explicit that sizing them to the fleet is
// what makes them defensible rather than arbitrary. So neither is a constant in
// the source: this reads aggregate fleet KV capacity and the engines' own
// idle-before-evict distribution off the replicas, takes the prompt
// bytes-per-token ratio off a sweep that has already run, and refuses to write a
// file if any of the three is missing.
//
// It has to run against a fleet that has been under load. The three KV residency
// families only appear with --kv-cache-metrics, which ADR-0001 sets from the
// first cell, and the idle-before-evict histogram stays empty until the fleet
// has actually evicted blocks. A fleet that has just come up has nothing to say
// here, which is why the calibration is measured during a run and carried
// forward in a file rather than re-derived at each bring-up: the fleet is taken
// down between policy passes.
//
// Do not enable --kv-cache-metrics to make this command work mid-experiment.
// Changing the engine configuration every cell is supposed to share invalidates
// every completed cell.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "calibrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		replicaSpecs  = flag.String("replicas", "", "the fleet to calibrate against, as the router's own -replicas spec")
		from          = flag.String("from", "", "a sweep directory to measure the prompt bytes-per-token ratio from; its cells carry the bytes offered and the tokens the engines reported")
		bytesPerToken = flag.Float64("prompt-bytes-per-token", 0,
			"the prompt bytes-per-token ratio, when there is no sweep to measure it from. Prefer -from: a figure typed in by hand is the guess this command exists to avoid")
		chosenTTL = flag.Duration("ttl", 0,
			"use this TTL instead of deriving one from the engine's idle-before-evict tail. "+
				"For a fleet with no residency history to derive from -- the histograms are empty until blocks have been evicted. "+
				"Recorded in the calibration as chosen rather than measured, so a run under it is never written up as a derived one")
		divergence = flag.String("divergence", "",
			"a belief-divergence reading from cmd/divergence, measured over a completed sweep. "+
				"It resizes the node cap: the fleet model is a ceiling, and the share of its belief the engines turned out to be honouring scales it down. "+
				"Without it the cap is the fleet model alone, which ADR-0006 says is a size derived from the fleet rather than a size anything has shown to be right")
		out     = flag.String("out", "runs/prefix-calibration.json", "where to write the calibration")
		timeout = flag.Duration("timeout", 30*time.Second, "how long to spend scraping the fleet")
	)
	flag.Parse()

	replicas, err := fleet.ParseSpecs(strings.Split(*replicaSpecs, ","))
	if err != nil {
		return err
	}
	if len(replicas) == 0 {
		return fmt.Errorf("-replicas is required: pass \"$(ops/fleet.sh replicas)\", or run via make calibrate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}

	// Capacity is summed off every replica's own reported cache configuration,
	// never extrapolated from one card: the figure moves between bring-ups, and
	// the index is being sized against the fleet that is actually up.
	capacity, err := characterize.ReadCapacity(ctx, client, replicas, characterize.HandComputed)
	if err != nil {
		return fmt.Errorf("read fleet KV capacity: %w", err)
	}

	baseURLs := make([]string, 0, len(replicas))
	for _, r := range replicas {
		baseURLs = append(baseURLs, r.BaseURL)
	}

	ratio, err := promptBytesPerToken(*from, *bytesPerToken)
	if err != nil {
		return err
	}

	observed, err := observedDivergence(*divergence)
	if err != nil {
		return err
	}

	measured := prefix.Calibration{
		FleetTokens:         capacity.Tokens,
		PromptBytesPerToken: ratio,
		BlockIdle:           prefix.ScrapeBlockIdle(ctx, client, baseURLs),
		BlockLifetime:       prefix.ScrapeBlockLifetime(ctx, client, baseURLs),
		ChosenTTL:           *chosenTTL,
		ObservedDivergence:  observed,
	}
	// Derived here rather than left for the router, so that a fleet that cannot
	// support a calibration fails now — with the fleet in front of whoever ran
	// this — instead of at the next bring-up.
	cfg, err := measured.Config()
	if err != nil {
		return err
	}
	if err := measured.Save(*out); err != nil {
		return err
	}

	fmt.Printf("prefix index calibrated from %d replicas\n", len(replicas))
	fmt.Printf("  aggregate fleet KV capacity   %d tokens\n", measured.FleetTokens)
	fmt.Printf("  prompt bytes per token        %.2f\n", measured.PromptBytesPerToken)
	fmt.Printf("  TTL                           %v (%s)\n", cfg.TTL, measured.TTLSource())
	if observed.Evidenced() {
		fmt.Printf("  observed belief divergence    %s\n", observed)
	}
	fmt.Printf("  node cap                      %d, from %s\n", cfg.NodeCap, measured.NodeCapSource())
	fmt.Printf("  -> node cap %d, TTL %v\n", cfg.NodeCap, cfg.TTL)
	if warning, disagrees := measured.CheckAgainstLifetime(cfg.TTL); disagrees {
		fmt.Printf("  ⚠️  %s\n", warning)
	}
	fmt.Printf("  written to %s; start the router with -prefix-calibration %s\n", *out, *out)
	return nil
}

// observedDivergence reads the belief-divergence reading the node cap is
// resized against, if one was given.
//
// Absent is not an error: the first sweep of prefix affinity necessarily runs
// against a cap nothing has measured yet, because the divergence is measured from
// the rows that sweep produces. The cap is then the fleet model, and
// NodeCapSource says so.
func observedDivergence(path string) (prefix.Divergence, error) {
	if path == "" {
		return prefix.Divergence{}, nil
	}
	return prefix.LoadDivergence(path)
}

// promptBytesPerToken measures the ratio off a sweep's cells, or takes the
// figure it was handed.
//
// Measured is strongly preferred and the flag exists for the case where no sweep
// has run yet. The two sides of the ratio come from different places on purpose
// — the bytes are what the harness sent and the tokens are what the engines say
// they processed — so measuring it captures the chat template and the tokenizer
// together, which no hand-computed figure does.
func promptBytesPerToken(from string, given float64) (float64, error) {
	if from == "" {
		if given <= 0 {
			return 0, fmt.Errorf("either -from or -prompt-bytes-per-token is required: the ratio converts every byte-denominated prefix figure into the engine's units, and it is measured rather than assumed")
		}
		return given, nil
	}

	cells, err := bench.LoadCells(from)
	if err != nil {
		return 0, err
	}
	var bytes int64
	readings := make([]vllmmetrics.Prefill, 0, len(cells))
	for _, cell := range cells {
		if !cell.PrefillRead {
			// A cell whose engines were not scraped contributes tokens nobody
			// counted. Skipped rather than folded in as zero, which would divide
			// its bytes by somebody else's tokens.
			continue
		}
		bytes += cell.PromptBytes
		readings = append(readings, cell.Prefill())
	}
	ratio, ok := prefix.MeasurePromptBytesPerToken(bytes, vllmmetrics.PoolPrefill(readings))
	if !ok {
		return 0, fmt.Errorf("no cell in %s carries both the prompt bytes it offered and the prompt tokens the engines reported, so no ratio can be measured there", from)
	}
	return ratio, nil
}
