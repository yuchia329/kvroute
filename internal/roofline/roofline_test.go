package roofline_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/roofline"
)

// The card, with measured roofs a little under its datasheet's: a ridge at
// 65e12 / 850e9 ≈ 76 FLOP per byte.
var rtx3090 = roofline.Ceilings{
	Card:      "NVIDIA GeForce RTX 3090",
	Measured:  roofline.Roof{Bandwidth: 850e9, Compute: 65e12},
	Datasheet: roofline.Roof{Bandwidth: 936e9, Compute: 71e12},
}

func timed(t *testing.T, annotation string, busy time.Duration) roofline.Timed {
	t.Helper()
	return roofline.Timed{Step: step(t, annotation), Span: busy, Busy: busy}
}

// decodeSteps is n steps of four sequences decoding past 1,028 tokens each, at
// the 6.344 ms a step took on the box.
func decodeSteps(t *testing.T, n int) []roofline.Timed {
	t.Helper()
	var steps []roofline.Timed
	for i := range n {
		sk := 4*1028 + 4*(i+1)
		steps = append(steps, timed(t,
			fmt.Sprintf("execute_4_context_0(sq0sk0sqsq0sqsk0)_generation_4(sq4sk%dsqsq4sqsk%d)", sk, sk),
			6344*time.Microsecond))
	}
	return steps
}

const prefill2048 = "execute_2048_context_1(sq2048sk2048sqsq4194304sqsk4194304)_generation_0(sq0sk0sqsq0sqsk0)"

// The ticket's claim, on the run's own numbers. A 2,048-token prefill that
// took 475.83 ms of GPU time does 2,070 FLOPs per byte, far right of the ridge;
// four sequences decoding do about 12, far left of it.
func TestDecodeIsBoundByBandwidthAndPrefillByCompute(t *testing.T) {
	steps := append(decodeSteps(t, 9), timed(t, prefill2048, 475830*time.Microsecond))

	r, err := roofline.Build(llama(t), rtx3090, steps)
	if err != nil {
		t.Fatalf("the roofline was refused: %v", err)
	}
	if len(r.Points) != 2 {
		t.Fatalf("%d points, want one decode batch and one prefill", len(r.Points))
	}
	for _, p := range r.Points {
		want := map[roofline.Kind]roofline.Bound{roofline.Decode: roofline.BandwidthBound, roofline.Prefill: roofline.ComputeBound}[p.Kind]
		if p.Bound != want {
			t.Errorf("%s is %s-bound at %.1f FLOP/byte, want %s-bound", p.Label(), p.Bound, p.Intensity(), want)
		}
	}
}

// Nothing runs faster than the datasheet allows. A 2,048-token prefill in 100
// ms would be 297 TFLOP/s on a card whose tensor cores peak at 71, so the work
// was overcounted or the time undercounted — a bug in the counting, not a
// result, and the roofline refuses to draw it.
func TestAPointAboveTheDatasheetRoofIsRefused(t *testing.T) {
	_, err := roofline.Build(llama(t), rtx3090, []roofline.Timed{timed(t, prefill2048, 100*time.Millisecond)})
	if err == nil {
		t.Fatal("a prefill at 297 TFLOP/s was drawn on a 71 TFLOP/s card")
	}
	if !strings.Contains(err.Error(), "prefill 2048 tokens") {
		t.Errorf("the refusal does not name the point: %v", err)
	}
}

// A point stands for one shape. Two kinds of step are neither: a step that
// prefilled three prompts while decoding a fourth, which is both at once, and
// the one step at a batch's end where a sequence that got its first token early
// has already finished — a batch of three nobody sent. They are counted, so the
// trace is accounted for, and not drawn.
func TestStepsOfNoOneShapeAreCountedButNotDrawn(t *testing.T) {
	steps := append(decodeSteps(t, 9),
		timed(t, "execute_769_context_3(sq768sk768sqsq196608sqsk196608)_generation_1(sq1sk257sqsq1sqsk257)", 180*time.Millisecond),
		timed(t, "execute_3_context_0(sq0sk0sqsq0sqsk0)_generation_3(sq3sk3099sqsq3sqsk3099)", 6130*time.Microsecond),
	)

	r, err := roofline.Build(llama(t), rtx3090, steps)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Points) != 1 || r.Points[0].Sequences != 4 {
		t.Errorf("drew %d points, want only the batch of four", len(r.Points))
	}
	if r.Undrawn != 2 {
		t.Errorf("%d steps counted as not drawn, want 2", r.Undrawn)
	}
}

// The report is the roofline as a table: every point with the steps it pools
// and the roof that bounds it, beside the ceilings it was placed against — the
// card's own and the datasheet's, each as bandwidth and compute.
func TestTheReportTabulatesEveryPointBesideItsCeilings(t *testing.T) {
	r, err := roofline.Build(llama(t), rtx3090, append(decodeSteps(t, 9), timed(t, prefill2048, 475830*time.Microsecond)))
	if err != nil {
		t.Fatal(err)
	}
	report := r.Report()

	rows := map[string]string{"| decode ×4 at ~1024 tokens | 9 |": "| bandwidth |", "| prefill 2048 tokens | 1 |": "| compute |"}
	for row, bound := range rows {
		line := lineWith(report, row)
		if line == "" {
			t.Errorf("no row starts %q:\n%s", row, report)
		} else if !strings.Contains(line, bound) {
			t.Errorf("the row %q does not say %q", line, bound)
		}
	}
	for _, ceiling := range []string{"850 GB/s", "65.0 TFLOP/s", "936 GB/s", "71.0 TFLOP/s"} {
		if !strings.Contains(report, ceiling) {
			t.Errorf("the report does not give the ceiling %s", ceiling)
		}
	}
}

func lineWith(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}
