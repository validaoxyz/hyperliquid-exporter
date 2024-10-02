package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func main() {
	// Load configuration
	cfg := config.LoadConfig()

	// Register Prometheus metrics
	metrics.RegisterMetrics()

	// Start Prometheus HTTP server
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.HandleFunc("/debug", debugHandler)
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

func debugHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	// Print local counts
	fmt.Fprintf(w, "Local Proposer Counts:\n")
	for proposer, count := range monitors.GetProposerCounts() {
		fmt.Fprintf(w, "%s: %d\n", proposer, count)
	}

	fmt.Fprintf(w, "\nPrometheus Metrics:\n")

	// Collect and write Prometheus metrics
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics.HLProposerCounter)
	registry.MustRegister(metrics.HLBlockHeightGauge)
	registry.MustRegister(metrics.HLApplyDurationGauge)

	metricFamilies, err := registry.Gather()
	if err != nil {
		fmt.Fprintf(w, "Error gathering metrics: %v\n", err)
		return
	}

	for _, mf := range metricFamilies {
		fmt.Fprintf(w, "%s\n", mf.GetName())
		for _, m := range mf.GetMetric() {
			fmt.Fprintf(w, "  %v\n", m)
		}
	}
}
