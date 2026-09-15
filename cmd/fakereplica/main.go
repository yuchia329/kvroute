// Command fakereplica runs one programmable stand-in for a vLLM replica.
//
// It exists so the router, the drivers and the harness can be exercised without
// a GPU. Its behaviour is held to the real engine's by the contract test in
// test/contract.
//
//	fakereplica -listen :8000 -id replica-0 -ttft 200ms -inter-token 20ms
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
	"github.com/yuchia329/kvroute/internal/httpserve"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fakereplica: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listen       = flag.String("listen", ":8000", "address to listen on")
		id           = flag.String("id", "fake", "replica id, used in logs")
		model        = flag.String("model", "", "model name echoed in responses")
		ttft         = flag.Duration("ttft", 0, "delay before the first token")
		interToken   = flag.Duration("inter-token", 0, "delay between subsequent tokens")
		outputTokens = flag.Int("output-tokens", 8, "tokens to generate when the request does not cap it")
		kvUtil       = flag.Float64("kv-utilization", 0, "value reported as vllm:kv_cache_usage_perc")
		cachedShare  = flag.Float64("cached-prompt-fraction", 0,
			"share of each request's prompt tokens the replica reports having served out of its KV cache, through usage.prompt_tokens_details.cached_tokens and vllm:prompt_tokens_cached_total. "+
				"A knob rather than a model of a cache: it is what lets the belief-divergence path be exercised end to end without a GPU, not a claim about how a real engine would have cached")
		omitDetails = flag.Bool("omit-prompt-tokens-details", false,
			"return usage with prompt_tokens_details null, as a replica started without --enable-prompt-tokens-details does. "+
				"It exists so ops/probe-usage.sh can be exercised against the failure it is there to catch, without a GPU")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	replica := fakereplica.New(fakereplica.Config{
		ID:                      *id,
		Model:                   *model,
		TTFT:                    *ttft,
		InterToken:              *interToken,
		OutputTokens:            *outputTokens,
		BatchKVOccupancy:        *kvUtil,
		CachedPromptFraction:    *cachedShare,
		OmitPromptTokensDetails: *omitDetails,
	})

	log.Info("fake replica listening", "addr", *listen, "id", *id, "ttft", *ttft, "inter_token", *interToken)
	return httpserve.Run(&http.Server{Addr: *listen, Handler: replica.Handler()}, 5*time.Second, log)
}
