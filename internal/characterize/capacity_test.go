package characterize_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/fleet"
)

// fakeFleet stands up n fake replicas, each reporting blocks GPU blocks.
func fakeFleet(t *testing.T, blocks ...int) []fleet.Replica {
	t.Helper()
	var replicas []fleet.Replica
	for i, count := range blocks {
		srv := httptest.NewServer(fakereplica.New(fakereplica.Config{
			NumGPUBlocks: count,
			BlockSize:    16,
		}).Handler())
		t.Cleanup(srv.Close)
		replicas = append(replicas, fleet.Replica{ID: replicaName(i), BaseURL: srv.URL})
	}
	return replicas
}

func readCapacity(t *testing.T, replicas []fleet.Replica) characterize.Capacity {
	t.Helper()
	c, err := characterize.ReadCapacity(context.Background(), http.DefaultClient, replicas, characterize.HandComputed)
	if err != nil {
		t.Fatalf("read capacity: %v", err)
	}
	return c
}

// The KV arithmetic idea.md §2 does by hand, checked here so the estimate the
// measurement is compared against is derived rather than pasted.
func TestPerTokenKVFootprintFollowsFromTheModelsGeometry(t *testing.T) {
	if got := characterize.Llama31_8B.BytesPerToken(); got != 128*1024 {
		t.Errorf("bytes per token = %d, want %d: 2 (K,V) x 8 KV heads x 128 dim x 2 bytes x 32 layers",
			got, 128*1024)
	}
	// 14 GiB of KV at 128 KiB a token. Grouped-query attention is the whole
	// reason this is 8 heads and not 32: using the attention head count would
	// put the estimate at a quarter of the tokens.
	if got := characterize.HandComputed.TokensPerReplica(); got != 114688 {
		t.Errorf("estimated tokens per replica = %d, want 114688", got)
	}
}

func TestCapacityIsSummedOverEveryReplicaRatherThanExtrapolatedFromOne(t *testing.T) {
	c := readCapacity(t, fakeFleet(t, 7872, 7872, 7872, 7872, 7872, 7872))

	if len(c.Replicas) != 6 {
		t.Fatalf("read %d replicas, want 6", len(c.Replicas))
	}
	if c.Replicas[0].Tokens != 125952 {
		t.Errorf("replica tokens = %d, want 125952: 7872 blocks of 16", c.Replicas[0].Tokens)
	}
	if c.Tokens != 6*125952 {
		t.Errorf("fleet tokens = %d, want %d", c.Tokens, 6*125952)
	}
	if !c.Uniform {
		t.Error("six identical replicas did not read as uniform")
	}
	if c.Bytes != int64(c.Tokens)*128*1024 {
		t.Errorf("fleet bytes = %d, want tokens x 128 KiB", c.Bytes)
	}
}

// The gap against the by-hand figure is the point of recording both, so it is
// computed rather than left to a reader with a calculator.
func TestCapacityRecordsTheGapAgainstTheHandComputedFigure(t *testing.T) {
	c := readCapacity(t, fakeFleet(t, 7872, 7872, 7872, 7872, 7872, 7872))

	if c.EstimatedTokens != 6*114688 {
		t.Errorf("estimated fleet tokens = %d, want %d", c.EstimatedTokens, 6*114688)
	}
	if c.Gap < 0.09 || c.Gap > 0.10 {
		t.Errorf("gap = %.4f, want ~0.098: 125,952 measured against 114,688 estimated", c.Gap)
	}
	// The gap turned back into bytes is what explains it: the estimate's
	// allowance for activations and CUDA graphs was too generous, and this is
	// the number that says by how much.
	if c.ImpliedKVBytesPerReplica <= characterize.HandComputed.KVBudgetBytes {
		t.Errorf("implied KV budget %d does not exceed the assumed %d, so the gap has no arithmetic explanation",
			c.ImpliedKVBytesPerReplica, characterize.HandComputed.KVBudgetBytes)
	}
}

// A fleet where one card is sized differently still has an aggregate, but that
// aggregate hides something and has to say so.
func TestCapacityFlagsAFleetWhoseReplicasDisagree(t *testing.T) {
	c := readCapacity(t, fakeFleet(t, 7872, 7463))

	if c.Uniform {
		t.Error("replicas reporting different capacities read as uniform")
	}
	if c.Tokens != (7872+7463)*16 {
		t.Errorf("fleet tokens = %d, want the sum of what each replica reported", c.Tokens)
	}
}

// WS is offered session tokens over aggregate fleet KV, so every point of the
// pressure grid moves when the measured denominator does.
func TestWorkingSetPointsRescaleOffTheMeasuredAggregate(t *testing.T) {
	c := readCapacity(t, fakeFleet(t, 7872, 7872, 7872, 7872, 7872, 7872))

	points := c.WorkingSets(characterize.DefaultSessionTokens, characterize.WorkingSetPoints)
	if len(points) != 4 {
		t.Fatalf("got %d WS points, want 4", len(points))
	}
	// 755,712 tokens at WS 1 is 369 sessions of 2k, against the 344 idea.md
	// computed off the estimate.
	if got := points[1]; got.Ratio != 1 || got.Sessions != 369 {
		t.Errorf("WS 1 is %+v, want 369 sessions", got)
	}
	if got := points[3]; got.Ratio != 8 || got.Sessions != 8*369 {
		t.Errorf("WS 8 is %+v, want 8x the WS 1 session count", got)
	}
}

// A replica whose engine does not publish the series must fail loudly. Reading
// zero capacity and carrying on would put a zero denominator under every
// working set ratio.
func TestCapacityRefusesAReplicaThatPublishesNoCacheConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("vllm:num_requests_running{model_name=\"m\"} 0\n"))
	}))
	t.Cleanup(srv.Close)

	_, err := characterize.ReadCapacity(context.Background(), http.DefaultClient,
		[]fleet.Replica{{ID: "replica-0", BaseURL: srv.URL}}, characterize.HandComputed)
	if err == nil {
		t.Fatal("read a capacity off a replica that publishes no cache config")
	}
}

// The engine reports the block count and the token count it derived from them.
// If those disagree the block size in the labels is not the one the tokens were
// computed with, and scaling off either would be wrong by that factor.
func TestCapacityRefusesAReplicaWhoseBlocksAndTokensDisagree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`vllm:cache_config_info{block_size="16",num_gpu_blocks="7872",kv_cache_size_tokens="999"} 1.0` + "\n"))
	}))
	t.Cleanup(srv.Close)

	_, err := characterize.ReadCapacity(context.Background(), http.DefaultClient,
		[]fleet.Replica{{ID: "replica-0", BaseURL: srv.URL}}, characterize.HandComputed)
	if err == nil {
		t.Fatal("accepted a replica whose block count and token count disagree")
	}
}
