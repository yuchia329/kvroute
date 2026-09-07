// Package contract_test asserts that the fake replica behaves like a real vLLM
// replica on the surfaces the router depends on.
//
// The fake is the project's single test seam: the router's ingress, the prefix
// index, the policies, inflight accounting and scraping are all tested against
// it. That is only
// meaningful if the fake is honest, so the same assertions run against a live
// replica of the pinned engine version. Point KVROUTE_CONTRACT_REPLICA at one
// during bring-up:
//
//	KVROUTE_CONTRACT_REPLICA=http://127.0.0.1:8000 go test ./test/contract/
//
// Without that variable the live half skips, so the suite still runs on a
// laptop with no GPU.
package contract_test

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

const (
	replicaEnv = "KVROUTE_CONTRACT_REPLICA"
	modelEnv   = "KVROUTE_CONTRACT_MODEL"

	defaultModel = "hugging-quants/Meta-Llama-3.1-8B-Instruct-AWQ-INT4"
	maxTokens    = 8
)

// TestFakeReplicaHonoursTheContract runs the contract against the fake.
func TestFakeReplicaHonoursTheContract(t *testing.T) {
	replica := fakereplica.New(fakereplica.Config{
		ID:            "contract",
		Model:         defaultModel,
		TTFT:          5 * time.Millisecond,
		InterToken:    5 * time.Millisecond,
		OutputTokens:  maxTokens,
		KVUtilization: 0.42,
	})
	srv := httptest.NewServer(replica.Handler())
	t.Cleanup(srv.Close)

	runContract(t, srv.URL, defaultModel)
}

// TestLiveReplicaHonoursTheContract runs the same contract against a replica of
// the pinned engine version. This is what makes every test above the seam mean
// something.
func TestLiveReplicaHonoursTheContract(t *testing.T) {
	baseURL := os.Getenv(replicaEnv)
	if baseURL == "" {
		t.Skipf("set %s to a live replica base URL to run the contract against the engine", replicaEnv)
	}
	model := os.Getenv(modelEnv)
	if model == "" {
		model = defaultModel
	}
	runContract(t, strings.TrimSuffix(baseURL, "/"), model)
}

// runContract is the contract itself: the behaviour the router relies on,
// asserted identically against the fake and against the engine.
//
// The subtests run in order on purpose — a live replica publishes some counters
// only once it has served a request, so the metrics assertions come last.
func runContract(t *testing.T, baseURL, model string) {
	t.Run("health endpoint answers", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/health")
		if err != nil {
			t.Fatalf("get /health: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("non-streaming completion", func(t *testing.T) {
		resp := postChat(t, baseURL, chatBody(model, false))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp.Body))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("content type = %q, want application/json", ct)
		}

		var got struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(readAll(t, resp.Body), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got.ID == "" {
			t.Error("response carries no id")
		}
		if got.Object != "chat.completion" {
			t.Errorf("object = %q, want chat.completion", got.Object)
		}
		if len(got.Choices) != 1 {
			t.Fatalf("got %d choices, want 1", len(got.Choices))
		}
		if got.Choices[0].Message.Role != "assistant" {
			t.Errorf("role = %q, want assistant", got.Choices[0].Message.Role)
		}
		if got.Choices[0].Message.Content == "" {
			t.Error("assistant message is empty")
		}
		if got.Choices[0].FinishReason == nil {
			t.Error("finish_reason is null on a completed response")
		}
		if got.Usage.PromptTokens <= 0 || got.Usage.CompletionTokens <= 0 {
			t.Errorf("usage not populated: %+v", got.Usage)
		}
	})

	t.Run("streaming completion", func(t *testing.T) {
		start := time.Now()
		resp := postChat(t, baseURL, chatBody(model, true))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp.Body))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("content type = %q, want text/event-stream", ct)
		}

		var (
			id           string
			chunks       int
			content      strings.Builder
			sawDone      bool
			sawFinish    bool
			firstChunkAt time.Duration
			scanner      = bufio.NewScanner(resp.Body)
			sawAfterDone bool
		)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			payload, found := strings.CutPrefix(line, "data: ")
			if !found {
				t.Errorf("SSE line is not a data field: %q", line)
				continue
			}
			if sawDone {
				sawAfterDone = true
			}
			if payload == "[DONE]" {
				sawDone = true
				continue
			}
			var chunk struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				Choices []struct {
					Delta struct {
						Role    *string `json:"role"`
						Content *string `json:"content"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				t.Fatalf("chunk %q is not JSON: %v", payload, err)
			}
			if chunks == 0 {
				firstChunkAt = time.Since(start)
				id = chunk.ID
			}
			chunks++
			if chunk.Object != "chat.completion.chunk" {
				t.Errorf("chunk object = %q, want chat.completion.chunk", chunk.Object)
			}
			if chunk.ID != id {
				t.Errorf("chunk id = %q, want %q: id must be stable across a stream", chunk.ID, id)
			}
			for _, c := range chunk.Choices {
				if c.Delta.Content != nil {
					content.WriteString(*c.Delta.Content)
				}
				if c.FinishReason != nil {
					sawFinish = true
				}
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("read stream: %v", err)
		}
		total := time.Since(start)

		if chunks == 0 {
			t.Fatal("stream carried no chunks")
		}
		if !sawDone {
			t.Error("stream did not terminate with data: [DONE]")
		}
		if sawAfterDone {
			t.Error("stream carried data after [DONE]")
		}
		if !sawFinish {
			t.Error("no chunk carried a finish_reason")
		}
		if content.Len() == 0 {
			t.Error("stream carried no content")
		}
		// The router's whole value proposition is that a token reaches the
		// client when the replica emits it. A replica that buffered its stream
		// would deliver every chunk at once and make TTFT unmeasurable, so the
		// stream has to arrive spread over time rather than in one burst.
		if spread := total - firstChunkAt; chunks >= 3 && spread < time.Millisecond {
			t.Errorf("all %d chunks arrived within %v of each other: replica is buffering, not streaming", chunks, spread)
		}
	})

	t.Run("malformed request errors with a JSON body", func(t *testing.T) {
		resp := postChat(t, baseURL, []byte(`{"model":`))
		defer resp.Body.Close()
		if resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Errorf("status = %d, want a 4xx", resp.StatusCode)
		}
		body := readAll(t, resp.Body)
		// The engine nests the error, and the router forwards this body to the
		// client untouched, so the shape is part of the contract.
		var got struct {
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("error body %q is not JSON: %v", body, err)
		}
		if got.Error == nil {
			t.Fatalf("error body is not nested under an \"error\" key: %s", body)
		}
		if got.Error.Message == "" {
			t.Errorf("error carries no message: %s", body)
		}
		if got.Error.Type == "" {
			t.Errorf("error carries no type: %s", body)
		}
		if got.Error.Code != resp.StatusCode {
			t.Errorf("error code %d disagrees with HTTP status %d", got.Error.Code, resp.StatusCode)
		}
	})

	t.Run("metrics expose every family the router depends on", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/metrics")
		if err != nil {
			t.Fatalf("get /metrics: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body := string(readAll(t, resp.Body))
		exposed := seriesNames(body)
		for _, family := range vllmmetrics.Required {
			for _, series := range family.SeriesNames() {
				if !exposed[series] {
					// Counters are the likely culprit: a Prometheus client
					// appends _total on exposition, so an engine build may
					// publish vllm:prompt_tokens_total where the spec recorded
					// vllm:prompt_tokens. Confirm what the replica actually
					// serves and fix the name in internal/vllmmetrics, which is
					// the one place it is declared.
					t.Errorf("metric %q (%s %s) is missing from %s: fix internal/vllmmetrics rather than ignoring this",
						series, family.Kind, family.Name, baseURL)
				}
			}
			if family.Kind == vllmmetrics.Histogram {
				assertHistogramIsConsistent(t, body, family.Name)
			}
		}
	})
}

// assertHistogramIsConsistent checks a histogram's exposition holds together:
// cumulative buckets must not decrease as le rises, and the +Inf bucket must
// equal the count. A histogram that fails this reads as garbage to any scrape
// that computes a quantile from it, while still looking populated.
func assertHistogramIsConsistent(t *testing.T, body, name string) {
	t.Helper()

	type bucket struct {
		le    float64
		value float64
	}
	var buckets []bucket
	var infinite, count float64
	var sawInf, sawCount bool

	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, rawValue, found := strings.Cut(line, " ")
		if !found || !strings.HasPrefix(series, name+"_") {
			continue
		}
		value, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			t.Errorf("%s carries an unparseable value %q", series, rawValue)
			continue
		}
		switch {
		case strings.HasPrefix(series, name+"_bucket"):
			rawLE := labelValue(series, "le")
			if rawLE == "+Inf" {
				infinite, sawInf = value, true
				continue
			}
			le, err := strconv.ParseFloat(rawLE, 64)
			if err != nil {
				t.Errorf("%s has an unparseable le %q", series, rawLE)
				continue
			}
			buckets = append(buckets, bucket{le, value})
		case strings.HasPrefix(series, name+"_count"):
			count, sawCount = value, true
		}
	}

	slices.SortFunc(buckets, func(a, b bucket) int { return cmp.Compare(a.le, b.le) })
	for i := 1; i < len(buckets); i++ {
		if buckets[i].value < buckets[i-1].value {
			t.Errorf("%s buckets are not cumulative: le=%g holds %g but le=%g holds %g",
				name, buckets[i-1].le, buckets[i-1].value, buckets[i].le, buckets[i].value)
		}
	}
	if !sawInf || !sawCount {
		return // the missing-series check above already reported this
	}
	if infinite != count {
		t.Errorf("%s: the +Inf bucket holds %g but _count is %g", name, infinite, count)
	}
	if len(buckets) > 0 && buckets[len(buckets)-1].value > infinite {
		t.Errorf("%s: bucket le=%g holds %g, more than the +Inf bucket's %g",
			name, buckets[len(buckets)-1].le, buckets[len(buckets)-1].value, infinite)
	}
	if count == 0 {
		return
	}

	// The sum has to be reachable from where the buckets say the observations
	// landed. If every observation is claimed to be at or below some bound,
	// the sum cannot exceed count times that bound. This is what catches a
	// histogram whose buckets are filled from something other than the values
	// it summed: structurally tidy, and nonsense to any quantile scrape.
	sum, ok := seriesValue(body, name+"_sum")
	if !ok {
		return
	}
	for _, b := range buckets {
		if b.value == count {
			if sum > count*b.le*(1+1e-9) {
				t.Errorf("%s: buckets put all %g observations at or below %g, but the sum is %g, which needs a mean of %g",
					name, count, b.le, sum, sum/count)
			}
			break
		}
	}
}

// seriesValue reads the value of a series with no le label, such as a _sum.
func seriesValue(body, name string) (float64, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		series, rawValue, found := strings.Cut(line, " ")
		if !found || !strings.HasPrefix(series, name) {
			continue
		}
		value, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

// labelValue pulls one label out of a series name such as
// `vllm:x_bucket{model_name="m",le="0.5"}`.
func labelValue(series, label string) string {
	_, labels, found := strings.Cut(series, "{")
	if !found {
		return ""
	}
	for part := range strings.SplitSeq(strings.TrimSuffix(labels, "}"), ",") {
		key, value, found := strings.Cut(part, "=")
		if found && key == label {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

// seriesNames collects the series present in a Prometheus text exposition.
func seriesNames(body string) map[string]bool {
	names := map[string]bool{}
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, _ := strings.Cut(line, " ")
		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}
		names[name] = true
	}
	return names
}

func chatBody(model string, stream bool) []byte {
	body, err := json.Marshal(map[string]any{
		"model":       model,
		"stream":      stream,
		"max_tokens":  maxTokens,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a terse assistant."},
			{"role": "user", "content": "Say hello."},
		},
	})
	if err != nil {
		panic(fmt.Sprintf("build chat body: %v", err))
	}
	return body
}

func postChat(t *testing.T, baseURL string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("post chat completions: %v", err)
	}
	return resp
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}
