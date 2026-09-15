// Command roofline places one replica's own prefill and decode steps on the
// roofline of the card they ran on (#23).
//
//	roofline drive -url http://127.0.0.1:8001 -model hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4
//	roofline report -dir runs/roofline
//
// drive sends the profile's load to a replica started under nsys with vLLM's
// cuda profiler, all of it inside one profile window, and closes the window
// when it is done. ops/roofline.sh starts that replica and runs this against it.
//
// report reads what that run left behind — the step exports, the measured
// ceilings and the model's config.json — and writes the roofline as a table and
// a chart. It needs no GPU, so a committed run can be redrawn from a checkout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuchia329/kvroute/internal/roofline"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: roofline drive|report [flags]")
	}
	switch args[0] {
	case "drive":
		return drive(args[1:])
	case "report":
		return report(args[1:])
	default:
		return fmt.Errorf("roofline: unknown command %q; want drive or report", args[0])
	}
}

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	dir := fs.String("dir", "runs/roofline", "a run of ops/roofline.sh: its step exports, ceilings.json and model-config.json")
	out := fs.String("out", "", "where to write roofline.md and roofline.svg; empty writes them into -dir")
	bandwidth := fs.Float64("datasheet-bandwidth", 936e9, "the card's datasheet memory bandwidth, bytes/s; the RTX 3090's is 936 GB/s")
	compute := fs.Float64("datasheet-compute", 71e12,
		"the card's datasheet fp16 tensor FLOP/s with fp32 accumulate; the RTX 3090's is 71 TFLOP/s (GA102 whitepaper)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		*out = *dir
	}

	config, err := os.Open(filepath.Join(*dir, "model-config.json"))
	if err != nil {
		return err
	}
	defer config.Close()
	model, err := roofline.ReadModel(config)
	if err != nil {
		return err
	}
	ceilings, err := readCeilings(filepath.Join(*dir, "ceilings.json"))
	if err != nil {
		return err
	}
	ceilings.Datasheet = roofline.Roof{Bandwidth: *bandwidth, Compute: *compute}

	steps, fromTrace, err := loadSteps(*dir)
	if err != nil {
		return err
	}

	r, err := roofline.Build(model, ceilings, steps)
	if err != nil {
		return err
	}
	md := r.Report()
	fmt.Print(md)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "roofline.md"), []byte(md), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "roofline.svg"), []byte(r.Chart()), 0o644); err != nil {
		return err
	}
	if !fromTrace {
		return nil
	}
	// Keep the steps beside the figures. nsys's own exports are hundreds of
	// megabytes and stay on the box; these few hundred kilobytes redraw the
	// roofline from a checkout.
	f, err := os.Create(filepath.Join(*out, "steps.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	return roofline.WriteSteps(f, steps)
}

// loadSteps prefers a run's own steps.jsonl, which is what a committed run
// keeps, and falls back to nsys's CSV exports, which only the box has.
func loadSteps(dir string) ([]roofline.Timed, bool, error) {
	if f, err := os.Open(filepath.Join(dir, "steps.jsonl")); err == nil {
		defer f.Close()
		steps, err := roofline.ReadSteps(f)
		return steps, false, err
	}
	ranges, err := os.Open(filepath.Join(dir, "steps_nvtx_gpu_proj_trace.csv"))
	if err != nil {
		return nil, false, err
	}
	defer ranges.Close()
	ops, err := os.Open(filepath.Join(dir, "steps_cuda_gpu_trace.csv"))
	if err != nil {
		return nil, false, err
	}
	defer ops.Close()
	steps, err := roofline.ReadTrace(ranges, ops)
	return steps, true, err
}

// readCeilings reads the roofs ops/roofline.sh measured on the card.
func readCeilings(path string) (roofline.Ceilings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return roofline.Ceilings{}, err
	}
	var c struct {
		Card      string  `json:"card"`
		Compute   float64 `json:"compute_flops_per_second"`
		Bandwidth float64 `json:"bandwidth_bytes_per_second"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return roofline.Ceilings{}, fmt.Errorf("roofline report: %s: %w", path, err)
	}
	if c.Compute <= 0 || c.Bandwidth <= 0 {
		return roofline.Ceilings{}, fmt.Errorf("roofline report: %s measured no ceilings", path)
	}
	return roofline.Ceilings{Card: c.Card, Measured: roofline.Roof{Bandwidth: c.Bandwidth, Compute: c.Compute}}, nil
}

// The default plan. Every decode batch fits the replica's KV cache whole —
// 125,952 tokens on this fleet — so no sequence is preempted and a batch decodes
// at the size it was sent at: 256 × (256 + 64) = 81,920 tokens and 48 × (2,048 +
// 64) = 101,376. The two contexts are two series rather than one because the
// KV cache a decode step reads grows with context and not with the weights, so
// at 2,048 tokens it is what keeps a large batch against the bandwidth roof.
const (
	defaultPrefill = "128,256,512,1024,2048,4096,8000"
	defaultDecode  = "1x256,2x256,4x256,8x256,16x256,32x256,64x256,128x256,256x256," +
		"1x2048,4x2048,16x2048,48x2048"
)

func drive(args []string) error {
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	url := fs.String("url", "", "the profiled replica, e.g. http://127.0.0.1:8001")
	model := fs.String("model", "", "the model name the replica serves")
	prefill := fs.String("prefill", defaultPrefill, "prompt lengths, in tokens, each prefilled alone")
	repeats := fs.Int("repeats", 3, "how many times each prefill length is sent")
	decode := fs.String("decode", defaultDecode, "decode batches as sequences x context tokens, decoded one after another")
	tokens := fs.Int("tokens", 64, "tokens each decoding sequence generates")
	seed := fs.Uint64("seed", 23, "seeds the random prompts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" || *model == "" {
		return fmt.Errorf("roofline drive: -url and -model are required")
	}
	lengths, err := parseInts(*prefill)
	if err != nil {
		return fmt.Errorf("roofline drive: -prefill: %w", err)
	}
	batches, err := parseBatches(*decode)
	if err != nil {
		return fmt.Errorf("roofline drive: -decode: %w", err)
	}
	plan := roofline.Plan{Model: *model, Prefill: lengths, Repeats: *repeats, Decode: batches, DecodeTokens: *tokens}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	began := time.Now()
	if err := roofline.Drive(ctx, *url, plan, rand.New(rand.NewPCG(*seed, *seed))); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "roofline: drove %d prefills and %d decode batches in %s\n",
		len(lengths)**repeats, len(batches), time.Since(began).Round(time.Second))
	return nil
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q is not a positive count", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// parseBatches reads "32x2048,48x2048" as batches of sequences x context.
func parseBatches(s string) ([]roofline.Batch, error) {
	var out []roofline.Batch
	for _, f := range strings.Split(s, ",") {
		seqs, ctx, ok := strings.Cut(strings.TrimSpace(f), "x")
		n, err1 := strconv.Atoi(seqs)
		c, err2 := strconv.Atoi(ctx)
		if !ok || err1 != nil || err2 != nil || n <= 0 || c <= 0 {
			return nil, fmt.Errorf("%q is not sequences x context, like 32x2048", f)
		}
		out = append(out, roofline.Batch{Sequences: n, Context: c})
	}
	return out, nil
}
