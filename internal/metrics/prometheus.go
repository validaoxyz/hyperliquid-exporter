package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

func StartPrometheusServer(ctx context.Context, port int) error {
	mux := http.NewServeMux()

	// middleware to log metrics requests with timeout
	metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("Metrics endpoint called from %s", r.RemoteAddr)

		// create context with timeout to prevent indefinite hangs
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// create channel to signal completion
		done := make(chan struct{})

		go func() {
			promhttp.Handler().ServeHTTP(w, r.WithContext(ctx))
			close(done)
		}()

		select {
		case <-done:
			logger.Debug("Metrics endpoint response completed")
		case <-ctx.Done():
			logger.Error("Metrics endpoint timed out after 30 seconds")
			http.Error(w, "Metrics collection timed out", http.StatusServiceUnavailable)
		}
	})

	mux.Handle("/metrics", metricsHandler)

	// health check endpoint for debugging
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		logger.Info("Starting Prometheus metrics server on port %d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Prometheus server error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.Background()); err != nil {
			logger.Error("Error shutting down Prometheus server: %v", err)
		}
	}()

	return nil
}
