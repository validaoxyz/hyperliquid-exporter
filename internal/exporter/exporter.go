package exporter

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func Start(cfg config.Config) {
	logger.Info("Starting Hyperliquid exporter...")

	logger.Info("Initializing block monitor...")
	monitors.StartBlockMonitor(cfg)

	logger.Info("Initializing proposal monitor...")
	monitors.StartProposalMonitor(cfg)

	logger.Info("Initializing validator monitor...")
	monitors.StartValidatorMonitor()

	logger.Info("Initializing version monitor...")
	monitors.StartVersionMonitor(cfg)

	logger.Info("Initializing update checker...")
	monitors.StartUpdateChecker()

	logger.Info("Setting up Prometheus metrics endpoint...")
	http.Handle("/metrics", promhttp.Handler())

	logger.Info("Exporter is now running. Listening on :8086")
	err := http.ListenAndServe(":8086", nil)
	if err != nil {
		logger.Error("Error starting HTTP server: %v", err)
	}
}
