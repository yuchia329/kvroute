package vllmmetrics_test

import (
	"context"
	"testing"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

func gauge(value string) string {
	return "# HELP " + vllmmetrics.BatchKVUsage + " x\n" +
		"# TYPE " + vllmmetrics.BatchKVUsage + " gauge\n" +
		vllmmetrics.BatchKVUsage + "{model_name=\"m\"} " + value + "\n"
}

func TestBatchKVOccupancyIsReadOffTheEnginesOwnGauge(t *testing.T) {
	got := vllmmetrics.ReadBatchOccupancy(context.Background(), nil, serving(t, gauge("0.83")))

	if !got.Read {
		t.Fatalf("the gauge was published and the reading says it was not: %+v", got)
	}
	if got.Fraction != 0.83 {
		t.Errorf("fraction = %v, want 0.83", got.Fraction)
	}
}

// An idle replica publishes 0, and a replica nobody could reach publishes
// nothing. Reporting the second as the first would publish a fleet that looks
// idle in the one column that says what the engines were doing with the load
// the router gave them.
func TestAnUnreachableReplicaIsUnreadRatherThanEmpty(t *testing.T) {
	idle := vllmmetrics.ReadBatchOccupancy(context.Background(), nil, serving(t, gauge("0")))
	if !idle.Read || idle.Fraction != 0 {
		t.Errorf("an idle replica read %+v, want a read zero", idle)
	}

	missing := vllmmetrics.ReadBatchOccupancy(context.Background(), nil, "http://127.0.0.1:1/metrics")
	if missing.Read {
		t.Errorf("an unreachable replica read %+v, want unread", missing)
	}
}

// The engine publishes a gauge whose name churned across releases, and it is
// not the one with "gpu" in it. A replica serving only the other name has to
// read as unread rather than as an empty cache.
func TestTheGPUCacheGaugeIsNotTheKVCacheGauge(t *testing.T) {
	body := "vllm:gpu_cache_usage_perc{model_name=\"m\"} 0.91\n"

	if got := vllmmetrics.ReadBatchOccupancy(context.Background(), nil, serving(t, body)); got.Read {
		t.Errorf("gpu_cache_usage_perc was read as batch KV occupancy: %+v", got)
	}
}
