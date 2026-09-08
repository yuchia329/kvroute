package vllmmetrics_test

import (
	"math"
	"testing"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// An exposition of the shape a replica serves: cumulative buckets, a trailing
// +Inf, and a sum and count that agree with them.
const idleExposition = `
# HELP vllm:kv_block_idle_before_evict_seconds How long a KV block sits idle before eviction.
# TYPE vllm:kv_block_idle_before_evict_seconds histogram
vllm:kv_block_idle_before_evict_seconds_bucket{model_name="llama",le="1"} 10
vllm:kv_block_idle_before_evict_seconds_bucket{model_name="llama",le="5"} 50
vllm:kv_block_idle_before_evict_seconds_bucket{model_name="llama",le="30"} 90
vllm:kv_block_idle_before_evict_seconds_bucket{model_name="llama",le="120"} 100
vllm:kv_block_idle_before_evict_seconds_bucket{model_name="llama",le="+Inf"} 100
vllm:kv_block_idle_before_evict_seconds_sum{model_name="llama"} 1400
vllm:kv_block_idle_before_evict_seconds_count{model_name="llama"} 100
`

func TestAHistogramIsReadAsItsBucketsSumAndCount(t *testing.T) {
	h := vllmmetrics.ReadHistogramFrom(idleExposition, "vllm:kv_block_idle_before_evict_seconds")

	if !h.Observed() {
		t.Fatalf("histogram not observed: %+v", h)
	}
	if h.Count != 100 || h.Sum != 1400 {
		t.Errorf("count/sum = %v/%v, want 100/1400", h.Count, h.Sum)
	}
	if len(h.Bounds) != 4 {
		t.Errorf("bounds = %v, want the four finite ones with +Inf dropped", h.Bounds)
	}
}

// The quantile is what the TTL is derived from, so it has to land in the right
// bucket and interpolate inside it rather than rounding to a boundary.
func TestAQuantileIsInterpolatedInsideItsBucket(t *testing.T) {
	h := vllmmetrics.ReadHistogramFrom(idleExposition, "vllm:kv_block_idle_before_evict_seconds")

	// The 95th observation sits in the (30, 120] bucket, five of the way into
	// the ten it holds: 30 + 0.5 * 90.
	got, ok := h.Quantile(0.95)
	if !ok {
		t.Fatal("p95 could not be located")
	}
	if math.Abs(got-75) > 0.001 {
		t.Errorf("p95 = %v, want 75", got)
	}
	// The median sits in (1, 5], forty observations wide, at its top.
	if got, ok := h.Quantile(0.5); !ok || math.Abs(got-5) > 0.001 {
		t.Errorf("p50 = %v (ok=%v), want 5", got, ok)
	}
}

// A tail that ran off the end of the buckets is reported as unlocatable. A TTL
// silently taken from the largest bound would be a number the exposition cannot
// support, and it would be wrong in the dangerous direction: too short, so the
// index forgets blocks the engine still holds.
func TestAQuantileInTheOverflowBucketIsNotLocated(t *testing.T) {
	overflowing := `
x_bucket{le="1"} 10
x_bucket{le="5"} 20
x_bucket{le="+Inf"} 100
x_sum 900
x_count 100
`
	h := vllmmetrics.ReadHistogramFrom(overflowing, "x")
	if _, ok := h.Quantile(0.99); ok {
		t.Error("a p99 past the last finite bound was reported as located")
	}
	if _, ok := h.Quantile(0.15); !ok {
		t.Error("a quantile inside the buckets was not located")
	}
}

// The KV residency families need --kv-cache-metrics. A replica started without
// it publishes nothing, and that has to read as nothing rather than as a
// histogram of zeros: everything downstream refuses to calibrate off an unread
// reading, and cannot refuse what it cannot tell apart.
func TestAnAbsentFamilyIsUnreadRatherThanEmpty(t *testing.T) {
	h := vllmmetrics.ReadHistogramFrom(idleExposition, "vllm:kv_block_lifetime_seconds")
	if h.Read {
		t.Errorf("a family nobody published read as present: %+v", h)
	}
	if _, ok := h.Quantile(0.99); ok {
		t.Error("an unread histogram answered a quantile")
	}

	empty := vllmmetrics.ReadHistogramFrom("x_bucket{le=\"1\"} 0\nx_sum 0\nx_count 0\n", "x")
	if !empty.Read {
		t.Error("a family published with nothing in it read as absent")
	}
	if empty.Observed() {
		t.Error("a family with no observations claims to have observed some")
	}
}

// One model per replica today, but a labelled family is pooled across its
// series rather than read off the first: taking one slice of a replica as the
// whole of it is the kind of error that produces a plausible number.
func TestLabelledSeriesArePooledRatherThanTakenFromTheFirst(t *testing.T) {
	twoModels := `
x_bucket{model_name="a",le="1"} 1
x_bucket{model_name="a",le="+Inf"} 2
x_sum{model_name="a"} 3
x_count{model_name="a"} 2
x_bucket{model_name="b",le="1"} 4
x_bucket{model_name="b",le="+Inf"} 8
x_sum{model_name="b"} 12
x_count{model_name="b"} 8
`
	h := vllmmetrics.ReadHistogramFrom(twoModels, "x")
	if h.Count != 10 || h.Sum != 15 {
		t.Errorf("count/sum = %v/%v, want 10/15 pooled across the label sets", h.Count, h.Sum)
	}
	if len(h.Cumulative) != 1 || h.Cumulative[0] != 5 {
		t.Errorf("cumulative = %v, want [5]", h.Cumulative)
	}
}
