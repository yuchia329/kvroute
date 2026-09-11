package roofline

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Timed is an engine step and the GPU time it took.
type Timed struct {
	Step Step
	// Span is from the step's first GPU operation starting to its last ending,
	// as nsys projects the step's NVTX range onto the GPU.
	Span time.Duration
	// Busy is the part of the span in which at least one of the step's
	// operations was running. The rest of the span is the GPU waiting on the
	// CPU, which is the engine's cost and not the model's.
	Busy time.Duration
}

// ReadTrace joins two of nsys's CSV exports of one trace into timed engine
// steps: nvtx_gpu_proj_trace, which places every NVTX range on the GPU's
// clock, and cuda_gpu_trace, which lists every GPU operation. An operation
// belongs to the step whose projected window it started in. Ranges that are not
// engine steps are skipped.
func ReadTrace(ranges, ops io.Reader) ([]Timed, error) {
	type window struct {
		step       Step
		start, end int64
	}
	var windows []window
	err := eachRow(ranges, "nvtx_gpu_proj_trace", []string{"Name", "Projected Start (ns)", "Projected Duration (ns)"},
		func(row []string, n []int64) {
			if s, ok := parseRangeName(row[0]); ok {
				windows = append(windows, window{step: s, start: n[1], end: n[1] + n[2]})
			}
		})
	if err != nil {
		return nil, err
	}

	var spans []interval
	err = eachRow(ops, "cuda_gpu_trace", []string{"Start (ns)", "Duration (ns)"},
		func(_ []string, n []int64) { spans = append(spans, interval{n[0], n[0] + n[1]}) })
	if err != nil {
		return nil, err
	}
	slices.SortFunc(spans, func(a, b interval) int { return int(a.start - b.start) })

	timed := make([]Timed, 0, len(windows))
	for _, w := range windows {
		first, _ := slices.BinarySearchFunc(spans, w.start, func(i interval, t int64) int { return int(i.start - t) })
		var mine []interval
		for _, op := range spans[first:] {
			if op.start >= w.end {
				break
			}
			mine = append(mine, interval{op.start, min(op.end, w.end)})
		}
		timed = append(timed, Timed{
			Step: w.step,
			Span: time.Duration(w.end - w.start),
			Busy: time.Duration(covered(mine)),
		})
	}
	return timed, nil
}

// WriteSteps writes timed steps as JSON, one step a line, in the order they
// ran. It is what a committed run keeps of a trace: nsys's own exports are
// hundreds of megabytes and stay on the box, and these few hundred kilobytes
// are everything the roofline is drawn from.
func WriteSteps(w io.Writer, steps []Timed) error {
	e := json.NewEncoder(w)
	for _, s := range steps {
		if err := e.Encode(stepLine{Step: s.Step.annotation(), SpanNs: s.Span.Nanoseconds(), BusyNs: s.Busy.Nanoseconds()}); err != nil {
			return fmt.Errorf("roofline: write steps: %w", err)
		}
	}
	return nil
}

// ReadSteps reads back what WriteSteps wrote.
func ReadSteps(r io.Reader) ([]Timed, error) {
	var steps []Timed
	d := json.NewDecoder(r)
	for {
		var line stepLine
		err := d.Decode(&line)
		if errors.Is(err, io.EOF) {
			return steps, nil
		}
		if err != nil {
			return nil, fmt.Errorf("roofline: read steps: %w", err)
		}
		s, ok := ParseStep(line.Step)
		if !ok {
			return nil, fmt.Errorf("roofline: read steps: %q is not an engine step", line.Step)
		}
		steps = append(steps, Timed{Step: s, Span: time.Duration(line.SpanNs), Busy: time.Duration(line.BusyNs)})
	}
}

type stepLine struct {
	Step   string `json:"step"`
	SpanNs int64  `json:"span_ns"`
	BusyNs int64  `json:"busy_ns"`
}

// annotation renders a step the way vLLM named it, so a step written out and
// read back is the step that ran.
func (s Step) annotation() string {
	return fmt.Sprintf("execute_%d_context_%d(sq%dsk%dsqsq%dsqsk%d)_generation_%d(sq%dsk%dsqsq%dsqsk%d)",
		s.Context.Tokens+s.Generation.Tokens,
		s.Context.Requests, s.Context.Tokens, s.Context.SeqLen, s.Context.QQ, s.Context.QK,
		s.Generation.Requests, s.Generation.Tokens, s.Generation.SeqLen, s.Generation.QQ, s.Generation.QK)
}

// parseRangeName reads a step from a projected range's name, which nsys
// prefixes with the range's NVTX domain and a colon — an empty domain for
// vLLM's, so ":execute_…".
func parseRangeName(name string) (Step, bool) {
	if s, ok := ParseStep(name); ok {
		return s, true
	}
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return ParseStep(name[i+1:])
	}
	return Step{}, false
}

type interval struct{ start, end int64 }

// covered is how much time a set of intervals, sorted by start, covers:
// operations overlapping on two streams are counted once.
func covered(spans []interval) int64 {
	var total, reach int64
	for _, s := range spans {
		if s.start > reach {
			reach = s.start
		}
		if s.end > reach {
			total += s.end - reach
			reach = s.end
		}
	}
	return total
}

// eachRow reads an nsys CSV export by its header, so a column nsys adds or
// moves in a later release does not shift what is read. The named columns are
// handed over as text and, where they parse, as integers.
func eachRow(r io.Reader, report string, columns []string, row func([]string, []int64)) error {
	c := csv.NewReader(r)
	c.ReuseRecord = true
	header, err := c.Read()
	if err != nil {
		return fmt.Errorf("roofline: read %s header: %w", report, err)
	}
	at := make([]int, len(columns))
	for i, name := range columns {
		at[i] = slices.Index(header, name)
		if at[i] < 0 {
			return fmt.Errorf("roofline: %s export has no %q column; is it `nsys stats -f csv -r %s`?", report, name, report)
		}
	}
	text := make([]string, len(columns))
	nums := make([]int64, len(columns))
	for {
		rec, err := c.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("roofline: read %s: %w", report, err)
		}
		for i, col := range at {
			text[i] = rec[col]
			nums[i], _ = strconv.ParseInt(rec[col], 10, 64)
		}
		row(text, nums)
	}
}
