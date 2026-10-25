// Package serve runs an HTTP server that drains cleanly when ctx is cancelled.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// ShutdownTimeout fits inside the pods' 15s terminationGracePeriodSeconds.
const ShutdownTimeout = 10 * time.Second

// Run serves h on addr until ctx is cancelled, then stops accepting
// connections and waits up to ShutdownTimeout for in-flight requests.
// It returns nil after a clean shutdown.
func Run(ctx context.Context, name, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return err // failed to start (e.g. port in use)
	case <-ctx.Done():
	}
	slog.Info("shutdown signal received, draining", "service", name, "timeout", ShutdownTimeout)
	// Fresh context: ctx is already cancelled.
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("http server drained", "service", name)
	return nil
}
