package exporter

import (
	"context"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func startBinaryMonitors(ctx context.Context, cfg config.Config, versionErrCh, updateErrCh chan<- error) {
	metrics.RegisterSource(metrics.SourceVersion, false)
	metrics.RegisterSource(metrics.SourceUpdate, false)
	if !cfg.EnableBinaryMetrics {
		logger.InfoComponent("system", "Binary metrics disabled; enable with --binary-metrics")
		return
	}

	if cfg.SkipVersionCheck {
		logger.InfoComponent("system", "Version monitor disabled by --skip-version-check")
	} else if _, err := monitors.NodeBinaryReady(cfg); err != nil {
		logger.WarningComponent("system", "Skipping version monitor: %v (use --skip-version-check to silence)", err)
	} else {
		logger.InfoComponent("system", "Initializing version monitor...")
		runMonitor("version", func() { monitors.StartVersionMonitor(ctx, cfg, versionErrCh) })
	}

	if cfg.SkipUpdateCheck {
		logger.InfoComponent("system", "Update checker disabled by --skip-update-check")
	} else {
		logger.InfoComponent("system", "Initializing update checker...")
		runMonitor("update_checker", func() { monitors.StartUpdateChecker(ctx, cfg, updateErrCh) })
	}
}
