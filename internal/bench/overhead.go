package bench

import (
	"compress/gzip"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/parquet-go/parquet-go"

	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/stats"
)

// LoadRouterRows reads a router's own per-request rows from path, in whichever
// form a run keeps them: the JSONL the router appends (.jsonl), that file
// gzipped for the repository (.jsonl.gz), or compacted (.parquet). The form is
// read off the name, and a name in none of them is an error rather than a guess.
func LoadRouterRows(path string) ([]record.Request, error) {
	switch {
	case strings.HasSuffix(path, ".parquet"):
		rows, err := parquet.ReadFile[record.Request](path)
		if err != nil {
			return nil, fmt.Errorf("bench: read %s: %w", path, err)
		}
		return rows, nil
	case strings.HasSuffix(path, ".jsonl.gz"):
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("bench: open %s: %w", path, err)
		}
		defer f.Close()
		unzipped, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("bench: %s is not gzipped: %w", path, err)
		}
		defer unzipped.Close()
		return decodeRows[record.Request](unzipped, path)
	case strings.HasSuffix(path, ".jsonl"):
		return decodeFile[record.Request](path)
	}
	return nil, fmt.Errorf("bench: %s is not router rows: they are kept as .jsonl, .jsonl.gz or .parquet", path)
}

// PolicyOverhead is the router's own cost under one policy: accept to first
// dispatch, over every request it dispatched.
//
// It is published so a reader can rule out the obvious alternative explanation
// of any win — that the cache-aware router simply added latency the baselines
// did not pay, or paid less of it. The cells cannot say: TTFT is measured at the
// client and folds the router's cost in with the fleet's. So this is read off the
// router's own rows, where the two were recorded apart.
type PolicyOverhead struct {
	Policy string `json:"policy"`
	// Dispatched is how many requests the router sent to a replica. A request it
	// never placed paid no dispatch cost to measure, and is not a zero here.
	Dispatched int   `json:"dispatched"`
	P50Ns      int64 `json:"p50_ns"`
	P99Ns      int64 `json:"p99_ns"`
	MaxNs      int64 `json:"max_ns"`
	// Tokenized is how many of those prompts the engine tokenized before exact
	// residency could decide, and the percentiles are how long that took. It is
	// inside the overhead above and carried apart from it, so the cost of knowing
	// exactly can be told from the cost of deciding. Zero under every other policy.
	Tokenized     int   `json:"tokenized"`
	TokenizeP50Ns int64 `json:"tokenize_p50_ns"`
	TokenizeP99Ns int64 `json:"tokenize_p99_ns"`
}

// RouterOverhead summarises router rows into each policy's overhead, in the
// comparison's policy order.
//
// A dispatched request is one with an overhead recorded: the router stamps it at
// the first dispatch and at no other point, and a duration measured with
// time.Since is never zero. The percentiles are stats.Quantile's, the project's
// one definition of a percentile, so these agree with /router/stats by rank.
func RouterOverhead(rows []record.Request) []PolicyOverhead {
	overheads := map[string][]int64{}
	tokenizes := map[string][]int64{}
	for _, r := range rows {
		if r.RouterOverheadNs <= 0 {
			continue
		}
		overheads[r.Policy] = append(overheads[r.Policy], r.RouterOverheadNs)
		if r.TokenizeNs > 0 {
			tokenizes[r.Policy] = append(tokenizes[r.Policy], r.TokenizeNs)
		}
	}

	present := map[string]bool{}
	for name := range overheads {
		present[name] = true
	}
	out := make([]PolicyOverhead, 0, len(present))
	for _, name := range comparisonOrder(present) {
		spent, asked := overheads[name], tokenizes[name]
		slices.Sort(spent)
		slices.Sort(asked)
		out = append(out, PolicyOverhead{
			Policy:        name,
			Dispatched:    len(spent),
			P50Ns:         stats.Quantile(spent, 0.50),
			P99Ns:         stats.Quantile(spent, 0.99),
			MaxNs:         spent[len(spent)-1],
			Tokenized:     len(asked),
			TokenizeP50Ns: stats.Quantile(asked, 0.50),
			TokenizeP99Ns: stats.Quantile(asked, 0.99),
		})
	}
	return out
}

// Overhead is what each policy cost the router, and the rows that was read
// from. The two travel together everywhere — a figure quoted without its
// sources cannot be traced back to the rows behind it — so they are one type,
// which reports and draws like the comparison and the map do.
type Overhead struct {
	Sources  []string
	Policies []PolicyOverhead
}

// OverheadFigure is the overhead's figure data, in the unit a plot labels it in.
type OverheadFigure struct {
	Sources  []string              `json:"sources"`
	Policies []PolicyOverheadPoint `json:"policies"`
}

// PolicyOverheadPoint is one policy's overhead as a plot draws it.
type PolicyOverheadPoint struct {
	Policy     string  `json:"policy"`
	Dispatched int     `json:"dispatched"`
	P50Us      float64 `json:"p50_us"`
	P99Us      float64 `json:"p99_us"`
	MaxUs      float64 `json:"max_us"`
	// Tokenized is how many prompts the engine tokenized for this policy, and the
	// percentiles are null under the policies that never ask — which is not a
	// tokenizer that answered instantly.
	Tokenized     int      `json:"tokenized"`
	TokenizeP50Us *float64 `json:"tokenize_p50_us"`
	TokenizeP99Us *float64 `json:"tokenize_p99_us"`
}

// Figure is the overhead's figure data.
func (o Overhead) Figure() OverheadFigure {
	f := OverheadFigure{Sources: orEmpty(o.Sources), Policies: []PolicyOverheadPoint{}}
	for _, p := range o.Policies {
		point := PolicyOverheadPoint{
			Policy:     p.Policy,
			Dispatched: p.Dispatched,
			P50Us:      microseconds(p.P50Ns),
			P99Us:      microseconds(p.P99Ns),
			MaxUs:      microseconds(p.MaxNs),
			Tokenized:  p.Tokenized,
		}
		if p.Tokenized > 0 {
			point.TokenizeP50Us = ref(microseconds(p.TokenizeP50Ns))
			point.TokenizeP99Us = ref(microseconds(p.TokenizeP99Ns))
		}
		f.Policies = append(f.Policies, point)
	}
	return f
}

// Report renders each policy's overhead as a table, with the files it was read
// from, so a figure quoted from it can be traced to its rows.
func (o Overhead) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Router overhead — accept to first dispatch\n\n")
	fmt.Fprintf(&b, "The router's own cost, read off its own rows rather than the client's: TTFT is\n")
	fmt.Fprintf(&b, "measured at the client with this inside it, so only the router's record can show\n")
	fmt.Fprintf(&b, "whether a policy won by paying less of it. Over every request the router\n")
	fmt.Fprintf(&b, "dispatched; one it never placed paid no dispatch cost. Tokenize is exact\n")
	fmt.Fprintf(&b, "residency asking the engine for the prompt's tokens, inside the overhead and shown\n")
	fmt.Fprintf(&b, "apart from it.\n\n")
	fmt.Fprintln(&b, "| policy | dispatched | p50 | p99 | max | tokenize p50 | tokenize p99 |")
	fmt.Fprintln(&b, "|---|---:|---:|---:|---:|---:|---:|")
	for _, p := range o.Policies {
		tokenizeP50, tokenizeP99 := "—", "—"
		if p.Tokenized > 0 {
			tokenizeP50, tokenizeP99 = us(p.TokenizeP50Ns), us(p.TokenizeP99Ns)
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s |\n", p.Policy, p.Dispatched,
			us(p.P50Ns), us(p.P99Ns), us(p.MaxNs), tokenizeP50, tokenizeP99)
	}
	fmt.Fprintf(&b, "\nRead from:\n\n")
	for _, source := range o.Sources {
		fmt.Fprintf(&b, "- `%s`\n", source)
	}
	return b.String()
}

// us renders a duration in microseconds, as ms renders one in milliseconds.
func us(ns int64) string {
	return fmt.Sprintf("%.0f µs", microseconds(ns))
}
