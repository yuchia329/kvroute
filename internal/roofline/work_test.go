package roofline_test

import (
	"os"
	"testing"

	"github.com/yuchia329/kvroute/internal/roofline"
)

// llama is the served model, read from its own config.json exactly as the HF
// snapshot on the box holds it.
func llama(t *testing.T) roofline.Model {
	t.Helper()
	f, err := os.Open("testdata/llama-3.1-8b-instruct-awq-int4.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := roofline.ReadModel(f)
	if err != nil {
		t.Fatalf("the served model's config.json was refused: %v", err)
	}
	return m
}

func step(t *testing.T, annotation string) roofline.Step {
	t.Helper()
	s, ok := roofline.ParseStep(annotation)
	if !ok {
		t.Fatalf("%q was not read as an engine step", annotation)
	}
	return s
}

// One sequence decoding its 1,024th token. Worked by hand from the model card:
//
//	linear  2 × 6,979,321,856 quantized weights × 1 token   = 13,958,643,712
//	logits  2 × 4,096 × 128,256 × 1 sampled row            =  1,050,673,152
//	attn    4 × 128 × 32 heads × 32 layers × 1,024 keys    =    536,870,912
//
//	weights 6,979,321,856 × (4 bits + 2.5 bytes per 128)   =  3,625,975,808
//	        lm_head 525,336,576 × 2 bytes                  =  1,050,673,152
//	KV      (1,024 read + 1 written) × 131,072 bytes       =    134,348,800
//	acts    4,456,448 per token + 264,704 per logits row   =      4,721,152
func TestADecodeStepAtBatchOneIsCountedFromTheModelsShapes(t *testing.T) {
	w := llama(t).Work(step(t, "execute_1_context_0(sq0sk0sqsq0sqsk0)_generation_1(sq1sk1024sqsq1sqsk1024)"))

	if w.FLOPs != 15_546_187_776 {
		t.Errorf("FLOPs = %d, want 15,546,187,776", w.FLOPs)
	}
	if w.Bytes != 4_815_718_912 {
		t.Errorf("bytes = %d, want 4,815,718,912", w.Bytes)
	}
}

// A 2,048-token prompt prefilled in one step from an empty cache. Its attention
// is causal: token i attends to i keys, 2,048 × 2,049 / 2 = 2,098,176 pairs in
// all, not the 2,048² the annotation's sqsk alone would suggest.
//
//	linear  13,958,643,712 × 2,048 tokens                  = 28,587,302,322,176
//	logits  one sampled row                                =      1,050,673,152
//	attn    524,288 per pair × 2,098,176 pairs             =  1,100,048,498,688
//
//	weights                                                =      4,676,648,960
//	KV      (2,048 read + 2,048 written) × 131,072 bytes   =        536,870,912
//	acts    4,456,448 × 2,048 tokens + 264,704             =      9,127,070,208
func TestAPrefillStepCountsOnlyTheCausalHalfOfItsAttention(t *testing.T) {
	w := llama(t).Work(step(t, "execute_2048_context_1(sq2048sk2048sqsq4194304sqsk4194304)_generation_0(sq0sk0sqsq0sqsk0)"))

	if w.FLOPs != 29_688_401_494_016 {
		t.Errorf("FLOPs = %d, want 29,688,401,494,016", w.FLOPs)
	}
	if w.Bytes != 14_340_590_080 {
		t.Errorf("bytes = %d, want 14,340,590,080", w.Bytes)
	}
}
