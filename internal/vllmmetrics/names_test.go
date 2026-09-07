package vllmmetrics_test

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// The line a live replica of the pinned engine version actually serves,
// trimmed to the labels that matter and the ones that make parsing hard.
const cacheConfig = `vllm:cache_config_info{block_size="16",cache_dtype="auto",enable_prefix_caching="True",` +
	`kv_cache_dtype_skip_layers="[1, 2]",kv_cache_size_tokens="125952",num_cpu_blocks="None",` +
	`num_gpu_blocks="7872",gpu_memory_utilization="0.9"} 1.0`

func TestLabelsReadsTheCacheGeometryOffTheSeriesTheEngineServes(t *testing.T) {
	labels, ok := vllmmetrics.Labels(cacheConfig, vllmmetrics.CacheConfigInfo)
	if !ok {
		t.Fatal("the series the engine serves was not found")
	}
	for label, want := range map[string]string{
		vllmmetrics.LabelNumGPUBlocks:      "7872",
		vllmmetrics.LabelBlockSize:         "16",
		vllmmetrics.LabelKVCacheSizeTokens: "125952",
		vllmmetrics.LabelGPUMemUtilization: "0.9",
	} {
		if got := labels[label]; got != want {
			t.Errorf("label %s = %q, want %q", label, got, want)
		}
	}
}

// The engine publishes list-valued labels. A split on every comma tears one in
// half and silently loses every label after it — including num_gpu_blocks,
// which sorts after it.
func TestLabelsSurvivesAListValuedLabel(t *testing.T) {
	labels, _ := vllmmetrics.Labels(cacheConfig, vllmmetrics.CacheConfigInfo)

	if got := labels["kv_cache_dtype_skip_layers"]; got != "[1, 2]" {
		t.Errorf("list label = %q, want [1, 2]", got)
	}
	if _, ok := labels[vllmmetrics.LabelNumGPUBlocks]; !ok {
		t.Error("the label after the list one was lost")
	}
}

// "Not published" and "published with nothing in it" must not read the same:
// the first is a version drift to fail loudly on, the second is a replica with
// an empty label set.
func TestLabelsDistinguishesAnAbsentSeriesFromAnEmptyOne(t *testing.T) {
	if _, ok := vllmmetrics.Labels("vllm:other{a=\"1\"} 1.0", vllmmetrics.CacheConfigInfo); ok {
		t.Error("reported a series that is not in the exposition")
	}
	labels, ok := vllmmetrics.Labels(vllmmetrics.CacheConfigInfo+"{} 1.0", vllmmetrics.CacheConfigInfo)
	if !ok {
		t.Fatal("an empty label set read as an absent series")
	}
	if len(labels) != 0 {
		t.Errorf("read %v, want no labels", labels)
	}
}

// Adding a metric to Required is what makes the contract test start checking
// for it, so the family the capacity reader depends on has to be in there.
func TestTheCacheConfigSeriesIsPartOfTheAssertedContract(t *testing.T) {
	for _, f := range vllmmetrics.Required {
		if f.Name == vllmmetrics.CacheConfigInfo {
			return
		}
	}
	t.Errorf("%s is read by the capacity reader but is not in Required, so no contract test asserts it", vllmmetrics.CacheConfigInfo)
}

func TestValueReadsACounterAndTellsAbsentApartFromZero(t *testing.T) {
	body := "# HELP vllm:prefix_cache_hits_total x\n" +
		"# TYPE vllm:prefix_cache_hits_total counter\n" +
		"vllm:prefix_cache_hits_total{model_name=\"m\"} 59632.0\n" +
		"vllm:prefix_cache_queries_total{model_name=\"m\"} 128525.0\n"

	got, ok := vllmmetrics.Value(body, "vllm:prefix_cache_hits_total")
	if !ok || got != 59632 {
		t.Errorf("hits = %v (%v), want 59632", got, ok)
	}
	// The HELP and TYPE lines name the series too, and reading one of those as
	// the value would report a counter that never moves.
	if _, ok := vllmmetrics.Value(body, "vllm:num_preemptions_total"); ok {
		t.Error("reported a value for a series the replica does not publish")
	}
	// A prefix of another series' name is not that series.
	if _, ok := vllmmetrics.Value(body, "vllm:prefix_cache"); ok {
		t.Error("matched a series by prefix rather than by name")
	}
}
