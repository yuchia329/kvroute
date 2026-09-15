package roofline_test

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/roofline"
)

// titles is the text of every <title> in an SVG — the tooltips a reader sees
// hovering over each mark. It fails the test if the chart is not well-formed
// SVG, because a browser shows nothing of one that is not.
func titles(t *testing.T, svg string) []string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(svg))
	var out []string
	var inTitle, sawSVG bool
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the chart is not well-formed: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			sawSVG = sawSVG || tok.Name.Local == "svg"
			inTitle = tok.Name.Local == "title"
		case xml.CharData:
			if inTitle {
				out = append(out, string(tok))
			}
		case xml.EndElement:
			inTitle = false
		}
	}
	if !sawSVG {
		t.Fatal("the chart is not an SVG")
	}
	return out
}

// The chart is the roofline drawn: both roofs, the card's own and its
// datasheet's, and every point, each named, so the figure can be read without
// the table beside it.
func TestTheChartDrawsBothRoofsAndNamesEveryPoint(t *testing.T) {
	r, err := roofline.Build(llama(t), rtx3090, append(decodeSteps(t, 9), timed(t, prefill2048, 475830*time.Microsecond)))
	if err != nil {
		t.Fatal(err)
	}
	named := strings.Join(titles(t, r.Chart()), "\n")

	for _, want := range []string{"measured roof", "datasheet roof", "decode ×4 at ~1024 tokens", "prefill 2048 tokens"} {
		if !strings.Contains(named, want) {
			t.Errorf("nothing on the chart is named %q; it names:\n%s", want, named)
		}
	}
}
