// Package httpserve runs a supervised HTTP process until it is signalled.
//
// Every component on the GPU box runs as a bare process behind a PID file, so
// SIGTERM has to drain in-flight streaming responses rather than cut them.
package httpserve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

// Run serves until SIGINT or SIGTERM arrives, then lets in-flight requests
// finish for at most grace.
func Run(srv *http.Server, grace time.Duration, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		stop()
		log.Info("draining", "grace", grace)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
