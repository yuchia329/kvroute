package disagg_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/disagg"
)

// The fields of the pinned model's config.json that its KV cache is shaped by,
// as the HF snapshot on the box has them.
const llama31_8B = `{
  "architectures": ["LlamaForCausalLM"],
  "hidden_size": 4096,
  "num_attention_heads": 32,
  "num_hidden_layers": 32,
  "num_key_value_heads": 8,
  "torch_dtype": "float16",
  "vocab_size": 128256
}`

// idea.md §2 did this by hand: 2 (K and V) x 8 heads x 128 dims x 2 bytes x 32
// layers = 131,072 bytes, 128 KiB a token.
func TestKVBytesPerTokenOfThePinnedModelIs128KiB(t *testing.T) {
	g, err := disagg.ParseModelConfig([]byte(llama31_8B))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := g.BytesPerToken(); got != 131072 {
		t.Errorf("bytes per token = %d, want 131072", got)
	}
	if g.Layers != 32 || g.KVHeads != 8 || g.HeadDim != 128 || g.DTypeBytes != 2 {
		t.Errorf("geometry = %+v, want 32 layers, 8 KV heads, 128 head dim, 2-byte dtype", g)
	}
}

// Some models size their heads independently of the hidden width and say so in
// head_dim; hidden_size / num_attention_heads would then be the wrong number.
// Here 4096 / 32 would be 128, and the config says 256.
func TestAnExplicitHeadDimIsTakenOverTheDerivedOne(t *testing.T) {
	g, err := disagg.ParseModelConfig([]byte(`{"hidden_size": 4096, "num_attention_heads": 32, "head_dim": 256,
		"num_hidden_layers": 32, "num_key_value_heads": 8, "torch_dtype": "float16"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if g.HeadDim != 256 {
		t.Errorf("head dim = %d, want the config's own 256", g.HeadDim)
	}
}

// A field read as zero would make every transfer free, and the arithmetic would
// conclude disaggregation costs nothing. So a config the geometry cannot be read
// out of is refused rather than read as small.
func TestAConfigTheGeometryCannotBeReadFromIsRefused(t *testing.T) {
	for name, config := range map[string]string{
		"unknown dtype":   `{"hidden_size": 4096, "num_attention_heads": 32, "num_hidden_layers": 32, "num_key_value_heads": 8, "torch_dtype": "float8_e4m3"}`,
		"no dtype":        `{"hidden_size": 4096, "num_attention_heads": 32, "num_hidden_layers": 32, "num_key_value_heads": 8}`,
		"no layers":       `{"hidden_size": 4096, "num_attention_heads": 32, "num_key_value_heads": 8, "torch_dtype": "float16"}`,
		"no heads":        `{"hidden_size": 4096, "num_hidden_layers": 32, "num_key_value_heads": 8, "torch_dtype": "float16"}`,
		"uneven head dim": `{"hidden_size": 4097, "num_attention_heads": 32, "num_hidden_layers": 32, "num_key_value_heads": 8, "torch_dtype": "float16"}`,
	} {
		if g, err := disagg.ParseModelConfig([]byte(config)); err == nil {
			t.Errorf("%s: parsed as %+v, %d bytes a token; want refused", name, g, g.BytesPerToken())
		}
	}
}
