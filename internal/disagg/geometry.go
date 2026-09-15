// Package disagg is idea.md §8's arithmetic: what it would cost to move a
// request's KV cache from the card that prefilled it to the card that decodes
// it, set against the prefill it follows, so disaggregating prefill and decode
// is settled by measurement rather than by building it.
//
// Every input is measured or read, never assumed: the bytes a token occupies
// come from the model's own config.json, the bandwidth from copies timed on the
// host's own links, and the prefill from the engine's own timer.
package disagg

import (
	"encoding/json"
	"fmt"
)

// KVGeometry is the shape of one token's KV cache, read off the model's own
// config.json.
type KVGeometry struct {
	Layers     int    `json:"layers"`
	KVHeads    int    `json:"kv_heads"`
	HeadDim    int    `json:"head_dim"`
	DType      string `json:"dtype"`
	DTypeBytes int    `json:"dtype_bytes"`
}

// BytesPerToken is what one token of context occupies in KV cache: a key and a
// value, per KV head, per layer.
func (g KVGeometry) BytesPerToken() int64 {
	return 2 * int64(g.Layers) * int64(g.KVHeads) * int64(g.HeadDim) * int64(g.DTypeBytes)
}

// modelConfig is the part of a Hugging Face config.json the KV cache is shaped
// by.
type modelConfig struct {
	HiddenSize        int    `json:"hidden_size"`
	NumAttentionHeads int    `json:"num_attention_heads"`
	NumHiddenLayers   int    `json:"num_hidden_layers"`
	NumKeyValueHeads  int    `json:"num_key_value_heads"`
	HeadDim           int    `json:"head_dim"`
	TorchDType        string `json:"torch_dtype"`
}

// dtypeBytes is the width of each dtype a KV cache is held in. The engine runs
// with kv_cache_dtype=auto, which keeps the cache in the model's own dtype.
var dtypeBytes = map[string]int{"float16": 2}

// ParseModelConfig reads a model's KV geometry out of its config.json.
//
// A field it cannot read is an error, never a zero: a geometry of zero bytes a
// token makes every transfer free, and the arithmetic would conclude that
// disaggregation costs nothing.
func ParseModelConfig(data []byte) (KVGeometry, error) {
	var c modelConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return KVGeometry{}, fmt.Errorf("disagg: config.json: %w", err)
	}
	width, known := dtypeBytes[c.TorchDType]
	headDim := c.HeadDim
	switch {
	case !known:
		return KVGeometry{}, fmt.Errorf("disagg: config.json: torch_dtype %q is not one whose width is known", c.TorchDType)
	case c.NumHiddenLayers <= 0 || c.NumKeyValueHeads <= 0:
		return KVGeometry{}, fmt.Errorf("disagg: config.json: needs num_hidden_layers and num_key_value_heads, got %+v", c)
	case headDim > 0:
		// Stated outright, which some models do because their heads are not
		// hidden_size / num_attention_heads wide.
	case c.NumAttentionHeads <= 0 || c.HiddenSize <= 0:
		return KVGeometry{}, fmt.Errorf("disagg: config.json: needs head_dim, or hidden_size and num_attention_heads to derive it, got %+v", c)
	case c.HiddenSize%c.NumAttentionHeads != 0:
		return KVGeometry{}, fmt.Errorf("disagg: config.json: hidden_size %d does not divide into %d attention heads", c.HiddenSize, c.NumAttentionHeads)
	default:
		headDim = c.HiddenSize / c.NumAttentionHeads
	}
	return KVGeometry{
		Layers:     c.NumHiddenLayers,
		KVHeads:    c.NumKeyValueHeads,
		HeadDim:    headDim,
		DType:      c.TorchDType,
		DTypeBytes: width,
	}, nil
}
