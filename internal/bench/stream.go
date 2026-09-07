package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"time"

	"github.com/yuchia329/kvroute/internal/stats"
)

// stream is what reading one SSE response told us about it.
//
// The timings here are the client's, and they are kept even though the router
// records its own and the replica publishes its own again. The difference
// between the three is what separates transport cost from inference cost, and a
// harness that recorded only one of them could not compute it.
type stream struct {
	firstByte time.Time
	bytes     int64
	tokens    int
	itlMean   time.Duration
	itlP50    time.Duration
	itlMax    time.Duration
}

// sseChunk is the little of a chunk the driver reads. Everything else about the
// response is the client's business, and the harness is not a client.
type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

var (
	dataPrefix = []byte("data: ")
	doneMarker = []byte("[DONE]")
)

// readStream consumes an SSE response, timing the first byte and the gap
// between successive token chunks.
//
// A chunk carrying no content — the opening chunk that announces the role, the
// closing chunk that carries only a finish reason, the usage chunk — is not a
// token and does not contribute a gap. Counting them would put two zero-length
// gaps into every response's inter-token latency.
func readStream(body io.Reader) (stream, error) {
	var s stream
	counted := &countingReader{r: body}
	scanner := bufio.NewScanner(counted)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var gaps []time.Duration
	var lastToken time.Time

	for scanner.Scan() {
		now := time.Now()
		if s.firstByte.IsZero() {
			s.firstByte = now
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, dataPrefix) {
			continue
		}
		payload := bytes.TrimSpace(line[len(dataPrefix):])
		if bytes.Equal(payload, doneMarker) {
			break
		}
		var chunk sseChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			// A chunk the harness cannot parse is not a reason to fail the
			// request: the bytes reached the client, which is what TTFT is
			// about. It simply does not count as a token.
			continue
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.Content == "" {
			continue
		}
		s.tokens++
		if !lastToken.IsZero() {
			gaps = append(gaps, now.Sub(lastToken))
		}
		lastToken = now
	}
	s.bytes = counted.n
	if err := scanner.Err(); err != nil {
		return s, err
	}

	if len(gaps) > 0 {
		var total time.Duration
		for _, g := range gaps {
			total += g
		}
		s.itlMean = total / time.Duration(len(gaps))
		slices.Sort(gaps)
		s.itlP50 = stats.Quantile(gaps, 0.50)
		s.itlMax = gaps[len(gaps)-1]
	}
	return s, nil
}

// countingReader totals the bytes actually read off the wire, so the row's
// response size is the response's size and not the sum of the lines the scanner
// chose to hand back.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
