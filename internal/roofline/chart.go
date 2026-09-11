package roofline

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Chart draws the roofline as an SVG: arithmetic intensity against achieved
// FLOP/s, both on log scales, under the card's measured roof and its
// datasheet's. Every mark carries a tooltip naming what it is, so the figure
// can be read without the table beside it. The background is painted, so the
// figure reads the same on a dark page as on a light one.
func (r Roofline) Chart() string {
	const (
		width, height            = 800.0, 480.0
		left, right, top, bottom = 76.0, 56.0, 44.0, 56.0
	)
	measured, datasheet := r.Ceilings.Measured, r.Ceilings.Datasheet

	loI, hiI, loR, hiR := 1.0, datasheet.Ridge()*10, measured.Attainable(1), datasheet.Compute
	for _, p := range r.Points {
		loI, hiI = min(loI, p.Intensity()), max(hiI, p.Intensity())
		loR, hiR = min(loR, p.Rate()), max(hiR, p.Rate())
	}
	xlo, xhi, ylo, yhi := decadeBelow(loI), decadeAbove(hiI), decadeBelow(loR), decadeAbove(hiR)
	x := func(v float64) float64 {
		return left + (math.Log10(v)-math.Log10(xlo))/(math.Log10(xhi)-math.Log10(xlo))*(width-left-right)
	}
	y := func(v float64) float64 {
		return height - bottom - (math.Log10(v)-math.Log10(ylo))/(math.Log10(yhi)-math.Log10(ylo))*(height-top-bottom)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" font-family="sans-serif" font-size="12">`+"\n",
		width, height, width, height)
	fmt.Fprintf(&b, `<rect width="%.0f" height="%.0f" fill="#ffffff"/>`+"\n", width, height)
	fmt.Fprintf(&b, `<text x="%.0f" y="24" font-size="15" font-weight="bold">Engine steps on %s</text>`+"\n", left, escape(r.Ceilings.Card))

	// Decade grid and tick labels.
	for v := xlo; v <= xhi*1.0001; v *= 10 {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="#e3e3e3"/>`+"\n", x(v), top, x(v), height-bottom)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle" fill="#444">%s</text>`+"\n", x(v), height-bottom+18, tick(v))
	}
	for v := ylo; v <= yhi*1.0001; v *= 10 {
		fmt.Fprintf(&b, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="#e3e3e3"/>`+"\n", left, y(v), width-right, y(v))
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" text-anchor="end" dominant-baseline="middle" fill="#444">%s</text>`+"\n", left-8, y(v), tick(v/1e12))
	}
	fmt.Fprintf(&b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="none" stroke="#888"/>`+"\n", left, top, width-left-right, height-top-bottom)
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" text-anchor="middle" fill="#222">arithmetic intensity (FLOP per byte of GPU memory traffic)</text>`+"\n",
		left+(width-left-right)/2, height-14)
	fmt.Fprintf(&b, `<text transform="translate(20 %.0f) rotate(-90)" text-anchor="middle" fill="#222">achieved TFLOP/s</text>`+"\n", top+(height-top-bottom)/2)

	// The roofs: bandwidth times intensity up to the ridge, compute beyond it.
	roof := func(name string, rf Roof, dash string) {
		start := max(xlo, ylo/rf.Bandwidth)
		fmt.Fprintf(&b, `<polyline points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="none" stroke="#222" stroke-width="2"%s><title>%s: %.0f GB/s, %.1f TFLOP/s, ridge at %s FLOP/byte</title></polyline>`+"\n",
			x(start), y(rf.Attainable(start)), x(rf.Ridge()), y(rf.Compute), x(xhi), y(rf.Compute), dash,
			name, rf.Bandwidth/1e9, rf.Compute/1e12, intensity(rf.Ridge()))
	}
	roof("datasheet roof", datasheet, ` stroke-dasharray="6 4" stroke-opacity="0.55"`)
	roof("measured roof", measured, "")
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.0f" stroke="#222" stroke-dasharray="2 3"><title>ridge: %s FLOP/byte</title></line>`+"\n",
		x(measured.Ridge()), y(measured.Compute), x(measured.Ridge()), height-bottom, intensity(measured.Ridge()))
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="#222">ridge %s</text>`+"\n", x(measured.Ridge())+4, height-bottom-6, intensity(measured.Ridge()))

	// The points, one series per kind of step and, for decode, per context.
	// Every label sits below its mark: a point lies just under its roof, so a
	// label above it would sit on the roof's line.
	series := r.series()
	// Each series labels on its own side of its marks. Within a series the
	// points are a factor of two apart and their labels never meet; two decode
	// series cross, and where they do their marks are at nearly the same
	// intensity — so those label to the left and to the right, which is the
	// separation that survives it, and prefill labels above.
	for _, p := range r.Points {
		at := slices.IndexFunc(series, func(s chartSeries) bool { return s.holds(p) })
		s := series[at]
		px, py := x(p.Intensity()), y(p.Rate())
		tip := fmt.Sprintf("%s: %s FLOP/byte, %.1f TFLOP/s, %.0f GB/s, %d steps",
			p.Label(), intensity(p.Intensity()), p.Rate()/1e12, p.Bandwidth()/1e9, p.Steps)
		if p.Kind == Prefill {
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="9" height="9" fill="%s"><title>%s</title></rect>`+"\n", px-4.5, py-4.5, s.color, escape(tip))
		} else {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4.5" fill="%s"><title>%s</title></circle>`+"\n", px, py, s.color, escape(tip))
		}
		if label := p.Mark(); label != "" {
			lx, ly, anchor := px, py-9, "middle"
			if p.Kind == Decode {
				if at%2 == 1 {
					lx, ly, anchor = px+8, py+4, "start"
				} else {
					lx, ly, anchor = px-8, py+4, "end"
				}
			}
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" text-anchor="%s" fill="%s">%s</text>`+"\n",
				lx, ly, anchor, s.color, escape(label))
		}
	}

	// Legend, inside the plot at the lower left. That corner is empty by
	// construction: a point there would be moving bytes slower than the card's
	// memory can, which is the one place the roofs say nothing can sit.
	lx := left + 24
	for i, s := range series {
		ly := height - bottom - 104 + float64(i)*22
		if s.kind == Prefill {
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="9" height="9" fill="%s"/>`+"\n", lx, ly-4.5, s.color)
		} else {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4.5" fill="%s"/>`+"\n", lx+4.5, ly, s.color)
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" dominant-baseline="middle" fill="#222">%s</text>`+"\n", lx+16, ly, escape(s.name))
	}
	b.WriteString("</svg>\n")
	return b.String()
}

type chartSeries struct {
	kind    Kind
	context int64
	name    string
	color   string
}

func (s chartSeries) holds(p Point) bool {
	return p.Kind == s.kind && (p.Kind == Prefill || p.Context == s.context)
}

// seriesColors are colour-blind-safe and distinct on white.
var seriesColors = []string{"#0072b2", "#009e73", "#cc79a7", "#56b4e9"}

// series is prefill, then one decode series per context, shortest first.
func (r Roofline) series() []chartSeries {
	out := []chartSeries{{kind: Prefill, name: "prefill, one prompt", color: "#d55e00"}}
	for _, p := range r.Points {
		if p.Kind != Decode || slices.ContainsFunc(out, func(s chartSeries) bool { return s.holds(p) }) {
			continue
		}
		n := len(out) - 1
		out = append(out, chartSeries{kind: Decode, context: p.Context,
			name: fmt.Sprintf("decode at ~%d tokens", p.Context), color: seriesColors[n%len(seriesColors)]})
	}
	return out
}

func decadeBelow(v float64) float64 { return math.Pow(10, math.Floor(math.Log10(v))) }
func decadeAbove(v float64) float64 { return math.Pow(10, math.Ceil(math.Log10(v))) }

// tick formats a decade: 0.1, 1, 10, 100, 1000, 10000.
func tick(v float64) string {
	if v < 1 {
		return fmt.Sprintf("%g", v)
	}
	return fmt.Sprintf("%.0f", v)
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func escape(s string) string { return xmlEscaper.Replace(s) }
