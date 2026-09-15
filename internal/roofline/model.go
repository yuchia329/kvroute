// Package roofline places the engine's own prefill and decode steps against
// the memory-bandwidth and compute limits of the card they ran on.
//
// A step's position on the roofline needs three things: the floating-point
// work it did, the bytes it had to move, and how long the GPU spent on it. The
// time is measured, by Nsight Systems. The work and the bytes are counted from
// the model's shapes and the step's own composition rather than read off
// hardware counters, because the box's driver reserves the counters for root
// (RmProfilingAdminOnly=1) and Nsight Compute cannot run without them. Counting
// is exact about what the model has to do; it is blind to what the kernels
// wasted doing it, which is the difference a counter would have shown.
package roofline

import (
	"encoding/json"
	"fmt"
	"io"
)

// Model is the shape of the served model: everything its work per step is
// counted from.
type Model struct {
	Layers       int64
	Hidden       int64
	Intermediate int64
	Heads        int64
	KVHeads      int64
	Vocab        int64
	// WeightBits is the width of a quantized weight, and GroupSize how many
	// share one fp16 scale and one zero point.
	WeightBits int64
	GroupSize  int64
}

// activationBytes is fp16: the width of every activation, KV entry and
// unquantized weight.
const activationBytes = 2

// ReadModel reads a Llama-shaped model from its Hugging Face config.json.
func ReadModel(r io.Reader) (Model, error) {
	var c struct {
		Layers       int64 `json:"num_hidden_layers"`
		Hidden       int64 `json:"hidden_size"`
		Intermediate int64 `json:"intermediate_size"`
		Heads        int64 `json:"num_attention_heads"`
		KVHeads      int64 `json:"num_key_value_heads"`
		Vocab        int64 `json:"vocab_size"`
		Quantization struct {
			Bits      int64 `json:"bits"`
			GroupSize int64 `json:"group_size"`
		} `json:"quantization_config"`
	}
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return Model{}, fmt.Errorf("roofline: read model config: %w", err)
	}
	m := Model{
		Layers: c.Layers, Hidden: c.Hidden, Intermediate: c.Intermediate,
		Heads: c.Heads, KVHeads: c.KVHeads, Vocab: c.Vocab,
		WeightBits: c.Quantization.Bits, GroupSize: c.Quantization.GroupSize,
	}
	return m, nil
}

// Work is what one step had to do: floating-point operations, and the fewest
// bytes of GPU memory it could have moved doing them.
type Work struct {
	FLOPs int64
	Bytes int64
}

// Work counts one step.
//
// FLOPs are the linear layers at two per weight per token, the logits at two
// per lm_head weight per sampled row, and causal attention at four per
// query-key pair per head dimension (QKᵀ and then PV).
//
// Bytes are the least traffic the step allows: every weight read once, every
// key and value the attention reads fetched once and every new one written
// once, and each linear layer's input read and output written. Norms and the
// embedding lookup are left out; together they are under a thousandth of the
// smallest step's bytes.
func (m Model) Work(s Step) Work {
	headDim := m.Hidden / m.Heads
	kvWidth := m.KVHeads * headDim

	// The four linear layers of one decoder layer: fused qkv, o, fused gate and
	// up, and down.
	perLayer := m.Hidden*(m.Hidden+2*kvWidth) + m.Hidden*m.Hidden +
		m.Hidden*2*m.Intermediate + m.Intermediate*m.Hidden
	linear := m.Layers * perLayer
	lmHead := m.Hidden * m.Vocab

	tokens := s.Context.Tokens + s.Generation.Tokens
	rows := s.Context.Requests + s.Generation.Requests
	keys := s.Context.SeqLen + s.Generation.SeqLen
	pairs := causalPairs(s.Context) + causalPairs(s.Generation)

	flops := 2*linear*tokens + 2*lmHead*rows + 4*headDim*m.Heads*m.Layers*pairs

	// A group of GroupSize weights is packed at WeightBits each, beside one fp16
	// scale and one zero point of WeightBits.
	quantized := linear / m.GroupSize * (m.GroupSize*m.WeightBits + 8*activationBytes + m.WeightBits) / 8
	weights := quantized + lmHead*activationBytes

	kvPerToken := 2 * kvWidth * activationBytes * m.Layers
	actsPerToken := m.Layers * activationBytes * ((m.Hidden + m.Hidden + 2*kvWidth) + // qkv
		(m.Hidden + m.Hidden) + // o
		(m.Hidden + 2*m.Intermediate) + // gate and up
		(m.Intermediate + m.Hidden)) // down
	actsPerRow := (m.Hidden + m.Vocab) * activationBytes

	bytes := weights + kvPerToken*(keys+tokens) + actsPerToken*tokens + actsPerRow*rows
	return Work{FLOPs: flops, Bytes: bytes}
}

// causalPairs is how many query-key pairs a phase's attention computes: each
// new token attends to every key before it and to itself. For one request of q
// new tokens over k keys that is qk − q(q−1)/2, which the annotation's sums give
// exactly across requests.
func causalPairs(p Phase) int64 {
	return p.QK - (p.QQ-p.Tokens)/2
}
