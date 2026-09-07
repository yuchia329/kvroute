// Package characterize establishes the measured facts every downstream number
// depends on, before any policy is compared.
//
// Four things are settled here and nowhere else: how much KV cache the fleet
// actually has, what latency the hardware delivers with nothing else running,
// what SLO follows from that floor, and whether the six replicas are
// interchangeable enough for a difference between policies to mean anything.
//
// They are one package because they are one measurement. Driving each replica
// on its own at two load levels produces the symmetry comparison and, from the
// pooled concurrency-1 rows, the latency floor the SLO is derived from. Running
// them separately would give an SLO derived from a fleet in one state and a
// symmetry verdict about a fleet in another.
package characterize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// Geometry is a model's per-token KV arithmetic, which is what turns a block
// count into a token count and a token count into bytes.
//
// It is spelled out rather than folded into a single bytes-per-token constant
// because the by-hand estimate and the measured figure have to be compared, and
// a comparison against a constant nobody can re-derive is not a comparison.
type Geometry struct {
	Layers int
	// KVHeads is the number of key/value heads, which grouped-query attention
	// makes smaller than the number of attention heads. Using the attention
	// head count here overstates the KV cache fourfold on this model.
	KVHeads         int
	HeadDim         int
	BytesPerElement int
}

// BytesPerToken is one token's KV footprint across every layer: a key and a
// value per head, per layer.
func (g Geometry) BytesPerToken() int64 {
	return int64(2 * g.KVHeads * g.HeadDim * g.BytesPerElement * g.Layers)
}

// Llama31_8B is the geometry of the pinned model, confirmed against its
// config.json: 32 layers, 8 KV heads, head dim 128, fp16 KV. AWQ quantizes the
// weights and not the KV cache, so the KV elements are still 2 bytes.
var Llama31_8B = Geometry{Layers: 32, KVHeads: 8, HeadDim: 128, BytesPerElement: 2}

// GiB is what the by-hand budget below is stated in.
const GiB = int64(1) << 30

// Estimate is the capacity figure computed by hand in idea.md §2, kept so the
// measured number has something to be checked against.
//
// KVBudgetBytes is the arithmetic's one soft input: 24 GB at
// --gpu-memory-utilization 0.9 is 21.6 GB, minus ~5.4 GB of AWQ weights and an
// assumed 1.5–2 GB for activations and CUDA graphs. That assumption is where
// the gap against the measured figure comes from, which is why the measured
// number is what everything scales off.
type Estimate struct {
	Geometry      Geometry
	KVBudgetBytes int64
}

// HandComputed is idea.md §2's estimate: ~14 GiB of KV per replica.
var HandComputed = Estimate{Geometry: Llama31_8B, KVBudgetBytes: 14 * GiB}

// TokensPerReplica is the estimated per-replica capacity in tokens.
func (e Estimate) TokensPerReplica() int {
	return int(e.KVBudgetBytes / e.Geometry.BytesPerToken())
}

// ReplicaCapacity is one replica's KV cache as the engine reports it.
type ReplicaCapacity struct {
	ReplicaID string `json:"replica_id"`
	BaseURL   string `json:"base_url"`

	// NumGPUBlocks and BlockSize are read off vllm:cache_config_info. Tokens is
	// their product, and EngineTokens is the engine's own token figure from the
	// same series: they are recorded separately so the derivation is checked
	// rather than assumed.
	NumGPUBlocks int `json:"num_gpu_blocks"`
	BlockSize    int `json:"block_size"`
	Tokens       int `json:"tokens"`
	EngineTokens int `json:"engine_tokens"`

	Bytes int64 `json:"bytes"`
}

// Capacity is the fleet's KV cache: what every working set ratio scales off.
type Capacity struct {
	Replicas      []ReplicaCapacity `json:"replicas"`
	Tokens        int               `json:"tokens"`
	Bytes         int64             `json:"bytes"`
	BytesPerToken int64             `json:"bytes_per_token"`
	// Uniform reports whether every replica came back with the same capacity.
	// Six agreeing replicas is a much stronger measurement than one, and a
	// fleet where one card is sized differently is a fleet whose aggregate
	// number hides something.
	Uniform bool `json:"uniform"`

	// The by-hand figure, kept beside the measured one.
	Estimate                  Estimate `json:"estimate"`
	EstimatedTokensPerReplica int      `json:"estimated_tokens_per_replica"`
	EstimatedTokens           int      `json:"estimated_tokens"`
	// Gap is measured over estimated, minus one: 0.05 means the fleet holds 5%
	// more than the arithmetic predicted.
	Gap float64 `json:"gap"`
	// ImpliedKVBytesPerReplica is what the measurement says the engine actually
	// left for KV. Set against Estimate.KVBudgetBytes it turns the gap from a
	// percentage into an explanation: the difference is entirely in how much
	// memory the estimate assumed activations and CUDA graphs would take.
	ImpliedKVBytesPerReplica int64 `json:"implied_kv_bytes_per_replica"`
}

// WorkingSetPoints is the pressure grid's WS axis from idea.md §6. The session
// counts behind these points are what the measured capacity rescales.
var WorkingSetPoints = []float64{0.25, 1, 3, 8}

// DefaultSessionTokens is the working set's per-session size, the divisor §2
// uses to turn a fleet capacity into a resident session count.
const DefaultSessionTokens = 2048

// WorkingSet is one point of the WS axis, in the units a workload generator
// takes: how many sessions of a given size that ratio calls for.
type WorkingSet struct {
	Ratio    float64 `json:"ratio"`
	Tokens   int     `json:"tokens"`
	Sessions int     `json:"sessions"`
}

// WorkingSets rescales the WS axis off the measured aggregate capacity.
//
// This is the only reason the capacity number is load-bearing: WS is offered
// session tokens over aggregate fleet KV, so every point of the pressure grid
// moves when the denominator does.
func (c Capacity) WorkingSets(sessionTokens int, ratios []float64) []WorkingSet {
	if sessionTokens <= 0 {
		sessionTokens = DefaultSessionTokens
	}
	points := make([]WorkingSet, 0, len(ratios))
	for _, ratio := range ratios {
		tokens := int(ratio * float64(c.Tokens))
		points = append(points, WorkingSet{Ratio: ratio, Tokens: tokens, Sessions: tokens / sessionTokens})
	}
	return points
}

// ReadCapacity reads every replica's KV cache geometry off its /metrics and
// aggregates it.
//
// All six, not one. A single replica's figure has already moved between two
// bring-ups of identical configuration, so a number taken from one card and
// multiplied by six is an assumption wearing a measurement's clothes.
func ReadCapacity(ctx context.Context, client *http.Client, replicas []fleet.Replica, estimate Estimate) (Capacity, error) {
	if len(replicas) == 0 {
		return Capacity{}, errors.New("characterize: no replicas to read capacity from")
	}
	if client == nil {
		client = http.DefaultClient
	}

	c := Capacity{
		BytesPerToken:             estimate.Geometry.BytesPerToken(),
		Estimate:                  estimate,
		EstimatedTokensPerReplica: estimate.TokensPerReplica(),
		Uniform:                   true,
	}
	c.EstimatedTokens = c.EstimatedTokensPerReplica * len(replicas)

	for _, r := range replicas {
		reported, err := readReplicaCapacity(ctx, client, r, c.BytesPerToken)
		if err != nil {
			return Capacity{}, err
		}
		if len(c.Replicas) > 0 && reported.Tokens != c.Replicas[0].Tokens {
			c.Uniform = false
		}
		c.Replicas = append(c.Replicas, reported)
		c.Tokens += reported.Tokens
		c.Bytes += reported.Bytes
	}

	if c.EstimatedTokens > 0 {
		c.Gap = float64(c.Tokens)/float64(c.EstimatedTokens) - 1
	}
	c.ImpliedKVBytesPerReplica = c.Bytes / int64(len(c.Replicas))
	return c, nil
}

func readReplicaCapacity(ctx context.Context, client *http.Client, r fleet.Replica, bytesPerToken int64) (ReplicaCapacity, error) {
	body, err := scrape(ctx, client, r.URL("/metrics", ""))
	if err != nil {
		return ReplicaCapacity{}, fmt.Errorf("characterize: %s: %w", r.ID, err)
	}
	labels, ok := vllmmetrics.Labels(body, vllmmetrics.CacheConfigInfo)
	if !ok {
		return ReplicaCapacity{}, fmt.Errorf("characterize: %s publishes no %s, so its KV capacity cannot be read; the engine version has drifted from the one internal/vllmmetrics declares",
			r.ID, vllmmetrics.CacheConfigInfo)
	}

	blocks, err := label(labels, vllmmetrics.LabelNumGPUBlocks, r.ID)
	if err != nil {
		return ReplicaCapacity{}, err
	}
	blockSize, err := label(labels, vllmmetrics.LabelBlockSize, r.ID)
	if err != nil {
		return ReplicaCapacity{}, err
	}
	engineTokens, err := label(labels, vllmmetrics.LabelKVCacheSizeTokens, r.ID)
	if err != nil {
		return ReplicaCapacity{}, err
	}

	tokens := blocks * blockSize
	// The engine reports both the blocks and the token count it derived from
	// them. They must agree: if they do not, the block size in the labels is
	// not the one the token figure was computed with, and every WS point built
	// on either would be wrong by that factor.
	if tokens != engineTokens {
		return ReplicaCapacity{}, fmt.Errorf("characterize: %s reports %d blocks of %d tokens (%d) but a cache of %d tokens; the two disagree, so neither can be scaled off",
			r.ID, blocks, blockSize, tokens, engineTokens)
	}
	return ReplicaCapacity{
		ReplicaID:    r.ID,
		BaseURL:      r.BaseURL,
		NumGPUBlocks: blocks,
		BlockSize:    blockSize,
		Tokens:       tokens,
		EngineTokens: engineTokens,
		Bytes:        int64(tokens) * bytesPerToken,
	}, nil
}

func label(labels map[string]string, name, replicaID string) (int, error) {
	raw, ok := labels[name]
	if !ok {
		return 0, fmt.Errorf("characterize: %s: %s carries no %s label", replicaID, vllmmetrics.CacheConfigInfo, name)
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("characterize: %s: %s label %s is %q, not a number: %w", replicaID, vllmmetrics.CacheConfigInfo, name, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("characterize: %s: %s label %s is %d, so the replica reports no KV cache at all", replicaID, vllmmetrics.CacheConfigInfo, name, value)
	}
	return value, nil
}

func scrape(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return string(body), nil
}
