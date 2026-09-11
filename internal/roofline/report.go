package roofline

import (
	"fmt"
	"strings"
)

// Report renders the roofline as markdown: how each point was placed, the
// ceilings, and every point against them.
func (r Roofline) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Roofline: %s\n\n", r.Ceilings.Card)
	b.WriteString("Each point is every engine step of one shape, pooled: the FLOPs and bytes the model's shapes say " +
		"those steps had to do, over the GPU time Nsight Systems measured them running. The work is counted rather " +
		"than read off performance counters, which this driver reserves for root, and it is the least the step " +
		"could have done — so a point's intensity is the model's, and only its height is the engine's.\n\n")

	b.WriteString("## Ceilings\n\n")
	b.WriteString("| | memory bandwidth | fp16 tensor compute | ridge |\n|---|---:|---:|---:|\n")
	for _, roof := range []struct {
		name string
		Roof
	}{{"measured on this card", r.Ceilings.Measured}, {"datasheet", r.Ceilings.Datasheet}} {
		fmt.Fprintf(&b, "| %s | %.0f GB/s | %.1f TFLOP/s | %s FLOP/byte |\n",
			roof.name, roof.Bandwidth/1e9, roof.Compute/1e12, intensity(roof.Ridge()))
	}

	b.WriteString("\n## Steps\n\n")
	b.WriteString("| point | steps | FLOP/byte | TFLOP/s | GB/s | bound | of the measured roof |\n")
	b.WriteString("|---|---:|---:|---:|---:|---|---:|\n")
	for _, p := range r.Points {
		fmt.Fprintf(&b, "| %s | %d | %s | %.1f | %.0f | %s | %.0f%% |\n",
			p.Label(), p.Steps, intensity(p.Intensity()), p.Rate()/1e12,
			p.Bandwidth()/1e9, p.Bound,
			100*p.Rate()/r.Ceilings.Measured.Attainable(p.Intensity()))
	}
	if r.Undrawn > 0 {
		fmt.Fprintf(&b, "\n%d steps are not drawn: prefill and decode in one step, prompts prefilled together, "+
			"or a decode batch's last few steps after one sequence finished early.\n", r.Undrawn)
	}
	return b.String()
}

// intensity formats FLOPs per byte to the precision it is worth: a tenth below
// a hundred, whole numbers above.
func intensity(v float64) string {
	if v < 100 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}
