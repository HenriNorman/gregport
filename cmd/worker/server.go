package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/HenriNorman/gregport/internal/platform/config"
)

func newServer(cfg config.WorkerConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.WorkerPort,
		Handler:           handler,
		ReadHeaderTimeout: cfg.WorkerReadHeaderTimeout,
		ReadTimeout:       cfg.WorkerReadTimeout,
		WriteTimeout:      cfg.WorkerWriteTimeout,
		IdleTimeout:       cfg.WorkerIdleTimeout,
	}
}

// runServer serves the health endpoint until ctx is cancelled, then drains
// in-flight requests within shutdownTimeout. The same pattern is used by the
// API so SIGTERM behaves identically in both binaries.
func runServer(ctx context.Context, srv *http.Server, ln net.Listener, shutdownTimeout time.Duration, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("worker health server shutting down", "timeout", shutdownTimeout.String())

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return fmt.Errorf("shutdown: %w", err)
		}

		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
