package vllmmetrics_test

import (
	"context"
	"testing"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

func gauge(value string) string {
	return "# HELP " + vllmmetrics.KVCacheUsage + " x\n" +
		"# TYPE " + vllmmetrics.KVCacheUsage + " gauge\n" +
		vllmmetrics.KVCacheUsage + "{model_name=\"m\"} " + value + "\n"
}

func TestKVUtilizationIsReadOffTheEnginesOwnGauge(t *testing.T) {
	got := vllmmetrics.ReadKVUtilization(context.Background(), nil, serving(t, gauge("0.83")))

	if !got.Read {
		t.Fatalf("the gauge was published and the reading says it was not: %+v", got)
	}
	if got.Fraction != 0.83 {
		t.Errorf("fraction = %v, want 0.83", got.Fraction)
	}
}

// An idle replica publishes 0, and a replica nobody could reach publishes
// nothing. Reporting the second as the first would let a scrape failure look
// like the emptiest cache in the fleet, which is where the spill rule would
// send every request.
func TestAnUnreachableReplicaIsUnreadRatherThanEmpty(t *testing.T) {
	idle := vllmmetrics.ReadKVUtilization(context.Background(), nil, serving(t, gauge("0")))
	if !idle.Read || idle.Fraction != 0 {
		t.Errorf("an idle replica read %+v, want a read zero", idle)
	}

	missing := vllmmetrics.ReadKVUtilization(context.Background(), nil, "http://127.0.0.1:1/metrics")
	if missing.Read {
		t.Errorf("an unreachable replica read %+v, want unread", missing)
	}
}

// The engine publishes a gauge whose name churned across releases, and it is
// not the one with "gpu" in it. A replica serving only the other name has to
// read as unread rather than as an empty cache.
func TestTheGPUCacheGaugeIsNotTheKVCacheGauge(t *testing.T) {
	body := "vllm:gpu_cache_usage_perc{model_name=\"m\"} 0.91\n"

	if got := vllmmetrics.ReadKVUtilization(context.Background(), nil, serving(t, body)); got.Read {
		t.Errorf("gpu_cache_usage_perc was read as KV utilization: %+v", got)
	}
}

func TestOverTheHighWaterMarkIsNeverAnswerableFromAnUnreadReplica(t *testing.T) {
	unread := vllmmetrics.KVUtilization{}
	if unread.Over(0.0) {
		t.Error("an unread reading fired a spill against a high-water mark of zero")
	}

	loaded := vllmmetrics.KVUtilization{Fraction: 0.9, Read: true}
	if !loaded.Over(0.8) {
		t.Error("0.9 did not read as over 0.8")
	}
	if loaded.Over(0.9) {
		t.Error("0.9 read as over 0.9: the mark is a threshold to exceed, not to reach")
	}
}
