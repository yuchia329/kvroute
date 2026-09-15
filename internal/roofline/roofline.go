package roofline

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"
)

// Roof is a card's two limits: how fast it moves bytes and how fast it does
// arithmetic. A step can go no faster than the lower of the two its arithmetic
// intensity allows.
type Roof struct {
	// Bandwidth is bytes per second to and from GPU memory.
	Bandwidth float64
	// Compute is fp16 tensor-core FLOPs per second.
	Compute float64
}

// Ridge is the arithmetic intensity, in FLOPs per byte, at which the two roofs
// meet. Left of it a step waits on memory; right of it, on arithmetic.
func (r Roof) Ridge() float64 { return r.Compute / r.Bandwidth }

// Attainable is the fastest a step of the given intensity can run.
func (r Roof) Attainable(intensity float64) float64 {
	return min(r.Compute, r.Bandwidth*intensity)
}

// Ceilings are the roofs the steps are placed against: the card's own,
// measured on it just before the profile, and its datasheet's.
type Ceilings struct {
	Card      string
	Measured  Roof
	Datasheet Roof
}

// Kind is what a point's steps did.
type Kind string

const (
	Prefill Kind = "prefill"
	Decode  Kind = "decode"
)

// Bound is which roof a point sits under.
type Bound string

const (
	BandwidthBound Bound = "bandwidth"
	ComputeBound   Bound = "compute"
)

// Point is one position on the roofline: every step of one shape, pooled. Their
// work is summed and their GPU time is summed, so a point is the rate the shape
// ran at, not the average of its steps' rates.
type Point struct {
	Kind Kind
	// Sequences is how many sequences each step decoded; one for a prefill.
	Sequences int64
	// Tokens is the new tokens each step prefilled; zero for a decode.
	Tokens int64
	// Context is each sequence's length: after the step for a prefill, and
	// rounded to a power of two for a decode, whose context grows by one a step.
	Context int64
	Steps   int
	Work    Work
	Busy    time.Duration
	Bound   Bound
}

// Intensity is the point's FLOPs per byte.
func (p Point) Intensity() float64 { return float64(p.Work.FLOPs) / float64(p.Work.Bytes) }

// Rate is the point's FLOPs per second of GPU time.
func (p Point) Rate() float64 { return float64(p.Work.FLOPs) / p.Busy.Seconds() }

// Bandwidth is the point's bytes of GPU memory traffic per second of GPU time.
func (p Point) Bandwidth() float64 { return float64(p.Work.Bytes) / p.Busy.Seconds() }

// Mark is the point's label on the chart itself, where there is room for its
// batch size or its prompt's tokens and no more. A chunk of a longer prompt has
// none: those crowd together near the compute roof, and the chart names each in
// its tooltip and the report gives it a row.
func (p Point) Mark() string {
	switch {
	case p.Kind == Decode:
		return fmt.Sprintf("×%d", p.Sequences)
	case p.Context > p.Tokens:
		return ""
	default:
		return fmt.Sprintf("%d", p.Tokens)
	}
}

// Label names the point's shape.
func (p Point) Label() string {
	switch {
	case p.Kind == Decode:
		return fmt.Sprintf("decode ×%d at ~%d tokens", p.Sequences, p.Context)
	case p.Context > p.Tokens:
		return fmt.Sprintf("prefill %d tokens over %d", p.Tokens, p.Context)
	default:
		return fmt.Sprintf("prefill %d tokens", p.Tokens)
	}
}

// Roofline is a profile's steps placed on a card's roofs.
type Roofline struct {
	Model    Model
	Ceilings Ceilings
	Points   []Point
	// Undrawn counts the steps of no one shape: prefill and decode in one step,
	// prompts prefilled together, and a decode batch's straggling tail.
	Undrawn int
}

// minDecodeSteps is the fewest steps a decode shape needs to be drawn. Every
// batch the plan sends decodes for dozens of steps; a shape seen fewer times
// than this is a batch's tail, where the sequences whose prompts were prefilled
// in different steps run out of tokens a few steps apart — a batch nobody sent,
// and one whose few steps would otherwise sit on the chart beside a real one.
const minDecodeSteps = 8

// Build pools a profile's steps into points. A prefill point is one prompt
// shape prefilled alone; a decode point is one batch size at one context,
// with no prompt in its steps.
func Build(m Model, c Ceilings, steps []Timed) (Roofline, error) {
	type shape struct {
		kind                          Kind
		sequences, tokens, contextLen int64
	}
	r := Roofline{Model: m, Ceilings: c}
	pooled := map[shape]*Point{}
	for _, s := range steps {
		var k shape
		switch ctx, gen := s.Step.Context, s.Step.Generation; {
		case gen.Requests == 0 && ctx.Requests == 1:
			k = shape{Prefill, 1, ctx.Tokens, ctx.SeqLen}
		case ctx.Requests == 0 && gen.Requests > 0:
			k = shape{Decode, gen.Requests, 0, powerOfTwo(float64(gen.SeqLen) / float64(gen.Requests))}
		default:
			r.Undrawn++
			continue
		}
		p := pooled[k]
		if p == nil {
			p = &Point{Kind: k.kind, Sequences: k.sequences, Tokens: k.tokens, Context: k.contextLen}
			pooled[k] = p
		}
		w := m.Work(s.Step)
		p.Steps++
		p.Work.FLOPs += w.FLOPs
		p.Work.Bytes += w.Bytes
		p.Busy += s.Busy
	}

	for _, p := range pooled {
		if p.Kind == Decode && p.Steps < minDecodeSteps {
			r.Undrawn += p.Steps
			continue
		}
		// Nothing runs faster than the datasheet allows, so a point above its roof
		// is the counting that is wrong: work overcounted, or time undercounted.
		if limit := c.Datasheet.Attainable(p.Intensity()); p.Rate() > limit {
			return Roofline{}, fmt.Errorf("roofline: %s ran at %.1f TFLOP/s, above the %.1f TFLOP/s the %s datasheet allows at %.0f FLOP/byte; "+
				"its work is overcounted or its time undercounted", p.Label(), p.Rate()/1e12, limit/1e12, c.Card, p.Intensity())
		}
		p.Bound = BandwidthBound
		if p.Intensity() >= c.Measured.Ridge() {
			p.Bound = ComputeBound
		}
		r.Points = append(r.Points, *p)
	}
	slices.SortFunc(r.Points, func(a, b Point) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Context, b.Context),
			cmp.Compare(a.Sequences, b.Sequences), cmp.Compare(a.Tokens, b.Tokens))
	})
	return r, nil
}

// powerOfTwo rounds a context length to the nearest power of two on a log
// scale, so a batch's steps, whose context grows by one token each, pool as one
// point: 257 to 362 tokens are all ~256.
func powerOfTwo(v float64) int64 {
	if v < 1 {
		return 1
	}
	return int64(math.Exp2(math.Round(math.Log2(v))))
}
