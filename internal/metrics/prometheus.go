package metrics

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

func StartPrometheusServer(ctx context.Context, port int) error {
	mux := http.NewServeMux()

	mux.Handle("/metrics", promhttp.Handler())

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
