package app

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// StartMetricsServer starts a background HTTP server exposing Prometheus metrics
// at /metrics and a simple liveness probe at /healthz, on the configured metrics
// address. It returns a shutdown function to call during graceful shutdown.
func (b *Base) StartMetricsServer() func(context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", b.Metrics.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:              b.Cfg.Observability.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			b.Logger.Error("metrics server failed", "error", err)
		}
	}()
	b.Logger.Info("metrics server listening", "addr", b.Cfg.Observability.MetricsAddr)
	return srv.Shutdown
}
