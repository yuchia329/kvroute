// Command fakereplica runs one programmable stand-in for a vLLM replica.
//
// It exists so the router, the drivers and the harness can be exercised without
// a GPU. Its behaviour is held to the real engine's by the contract test in
// test/contract.
//
//	fakereplica -listen :8000 -id replica-0 -ttft 200ms -inter-token 20ms
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuchia329/kvroute/internal/fakereplica"
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
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	replica := fakereplica.New(fakereplica.Config{
		ID:            *id,
		Model:         *model,
		TTFT:          *ttft,
		InterToken:    *interToken,
		OutputTokens:  *outputTokens,
		KVUtilization: *kvUtil,
	})

	srv := &http.Server{Addr: *listen, Handler: replica.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Info("fake replica listening", "addr", *listen, "id", *id, "ttft", *ttft, "inter_token", *interToken)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}
