package roofline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
)

// Plan is the load a profile is taken under. Prefill and decode are driven
// apart, so that almost every engine step in the trace is purely one or the
// other and lands on the roofline as what it is.
type Plan struct {
	// Model is the name the replica serves the model under.
	Model string
	// Prefill lists prompt lengths in tokens. Each is sent alone and asks for a
	// single token, so the steps it causes do nothing but prefill.
	Prefill []int
	// Repeats is how many times each prefill length is sent.
	Repeats int
	// Decode lists the batches to decode, one after another.
	Decode []Batch
	// DecodeTokens is how many tokens every decoding sequence generates.
	DecodeTokens int
}

// Batch is one decode point: Sequences decoding together, each over a prompt of
// Context tokens.
type Batch struct {
	Sequences int
	Context   int
}

// The warm-up is one short request before the window opens: the engine's first
// request after startup pays for whatever it initialises lazily, and that is not
// a step of the model.
const (
	warmupPrompt = 64
	warmupTokens = 4
)

// Drive sends a plan to one replica inside a single profile window, opened with
// POST /start_profile and closed with POST /stop_profile. The window is closed
// however the plan ends, because a profiler left capturing never writes its
// trace.
func Drive(ctx context.Context, base string, p Plan, rng *rand.Rand) error {
	c := client{base: strings.TrimSuffix(base, "/"), model: p.Model}
	if err := c.complete(ctx, prompt(rng, warmupPrompt), warmupTokens); err != nil {
		return fmt.Errorf("roofline: warm-up: %w", err)
	}
	if err := c.post(ctx, "/start_profile"); err != nil {
		return err
	}
	err := p.run(ctx, c, rng)
	if stop := c.post(context.WithoutCancel(ctx), "/stop_profile"); err == nil {
		err = stop
	}
	return err
}

func (p Plan) run(ctx context.Context, c client, rng *rand.Rand) error {
	for _, length := range p.Prefill {
		for range p.Repeats {
			if err := c.complete(ctx, prompt(rng, length), 1); err != nil {
				return fmt.Errorf("roofline: prefill of %d tokens: %w", length, err)
			}
		}
	}
	for _, b := range p.Decode {
		if err := p.decode(ctx, c, rng, b); err != nil {
			return fmt.Errorf("roofline: decode batch of %d at %d tokens of context: %w", b.Sequences, b.Context, err)
		}
	}
	return nil
}

// decode sends a batch's sequences all at once and waits for every one of them.
// Sent together and asked for the same tokens, they prefill, then decode in
// lockstep as one batch of that size, and finish together.
func (p Plan) decode(ctx context.Context, c client, rng *rand.Rand, b Batch) error {
	prompts := make([][]int, b.Sequences)
	for i := range prompts {
		prompts[i] = prompt(rng, b.Context)
	}
	errs := make([]error, len(prompts))
	var wg sync.WaitGroup
	for i := range prompts {
		wg.Go(func() { errs[i] = c.complete(ctx, prompts[i], p.DecodeTokens) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// prompt is length random token ids, drawn above the special tokens and below
// the vocabulary's end. Random so that no prompt is one the replica has cached
// (ADR-0004): a prefill served from the prefix cache is not a prefill.
func prompt(rng *rand.Rand, length int) []int {
	ids := make([]int, length)
	for i := range ids {
		ids[i] = 1000 + rng.IntN(119_000)
	}
	return ids
}

type client struct {
	base  string
	model string
}

func (c client) post(ctx context.Context, path string) error {
	return c.do(ctx, path, nil)
}

// complete asks for maxTokens with EOS ignored, so a sequence generates exactly
// what it was asked for and a batch sent together finishes together.
func (c client) complete(ctx context.Context, prompt []int, maxTokens int) error {
	return c.do(ctx, "/v1/completions", map[string]any{
		"model":      c.model,
		"prompt":     prompt,
		"max_tokens": maxTokens,
		"ignore_eos": true,
	})
}

func (c client) do(ctx context.Context, path string, body any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}
