package main

import (
    "log"
    "net/http"

    "github.com/validaoxyz/hyperliquid-exporter/internal/config"
    "github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
    "github.com/validaoxyz/hyperliquid-exporter/internal/monitors"

    "github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
    // Load configuration
    cfg := config.LoadConfig()

    // Register Prometheus metrics
    metrics.RegisterMetrics()

    // Start Prometheus HTTP server
    go func() {
        http.Handle("/metrics", promhttp.Handler())
        log.Println("Starting Prometheus HTTP server on port 8086")
        log.Fatal(http.ListenAndServe(":8086", nil))
    }()

    // Start monitors
    monitors.StartProposalMonitor(cfg)
    monitors.StartBlockMonitor(cfg)
    monitors.StartValidatorMonitor()
    monitors.StartVersionMonitor(cfg)
    monitors.StartUpdateChecker()

    // Keep the application running
    select {}
}
