package bench_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
)

// TestAFixtureCellsRowsSummariseToFiguresWorkedByHand reads one cell's request
// rows, written by hand, and summarises them the way every cell record is
// summarised. Every expected figure below was worked on paper from
// testdata/rows.jsonl, against the derived SLO of TTFT < 990 ms and inter-token
// p50 < 24 ms:
//
//   - w1 is warm-up, and set aside from every count and percentile
//   - r1 (TTFT 300 ms) and r5 (400 ms) met the SLO
//   - r2 (500 ms, but 30 ms between tokens) and r3 (1.2 s to first token) did
//     not, and are violations rather than failures
//   - r4 failed, and is in no latency figure
//
// The window runs from r1's start at 1 s to r5's last byte at 6 s: 5 s. So
// goodput is 2 / 5 s = 0.4, throughput 4 / 5 s = 0.8, and nearest rank over the
// four successes' TTFTs {300, 400, 500, 1200} puts p50 at 400 ms and p99 at 1.2 s.
// One failure in five is 20%, far past the 1% threshold.
func TestAFixtureCellsRowsSummariseToFiguresWorkedByHand(t *testing.T) {
	f, err := os.Open("testdata/rows.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []bench.Result
	for decoder := json.NewDecoder(f); ; {
		var r bench.Result
		if err := decoder.Decode(&r); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}

	got := bench.Summarize(rows, bench.SummaryOptions{SLO: derivedSLO})

	if got.Warmup != 1 || got.Requests != 5 || got.Successes != 4 || got.Failed != 1 || got.SLOViolations != 2 {
		t.Errorf("counts = warm-up %d, requests %d, successes %d, failed %d, violations %d; want 1, 5, 4, 1, 2",
			got.Warmup, got.Requests, got.Successes, got.Failed, got.SLOViolations)
	}
	if time.Duration(got.WindowNs) != 5*time.Second {
		t.Errorf("window = %v, want 5s", time.Duration(got.WindowNs))
	}
	if got.GoodputRPS != 0.4 || got.ThroughputRPS != 0.8 {
		t.Errorf("goodput %v and throughput %v, want 0.4 and 0.8", got.GoodputRPS, got.ThroughputRPS)
	}
	if time.Duration(got.TTFTP50Ns) != 400*time.Millisecond || time.Duration(got.TTFTP99Ns) != 1200*time.Millisecond {
		t.Errorf("TTFT p50 %v and p99 %v, want 400ms and 1.2s", time.Duration(got.TTFTP50Ns), time.Duration(got.TTFTP99Ns))
	}
	if got.FailureRate != 0.2 || !got.Flagged {
		t.Errorf("failure rate %v, flagged %v; want 0.2 and flagged", got.FailureRate, got.Flagged)
	}
}
