package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/webapi"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/webchannel"
)

func startHealthServer(
	ctx context.Context,
	address string,
	ready *atomic.Bool,
	logger *slog.Logger,
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	return startServer(ctx, "health", address, mux, logger)
}

// startAPIServer serves the agent HTTP API (read-only execution-graph
// projections, interactive control, and the live event stream) on its own port,
// separate from the health/probe port so it can be exposed via a Service.
// Disabled when address is empty.
func startAPIServer(
	ctx context.Context,
	address string,
	runtime aurora.Runtime,
	channel *webchannel.Channel,
	logger *slog.Logger,
) *http.Server {
	if strings.TrimSpace(address) == "" {
		return nil
	}
	return startServer(ctx, "api", address, webapi.Handler(runtime, channel), logger)
}

func startServer(ctx context.Context, name, address string, handler http.Handler, logger *slog.Logger) *http.Server {
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error(name+" server", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server
}
