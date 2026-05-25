package monitors

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// processNames are the hl- processes we look up via /proc. Both run on
// validator and non-validator nodes; on container deployments either may
// be absent. The monitor publishes hl_node_process_up=0 for missing ones
// so operators can alert on "expected hl-node process not running".
var processNames = []string{"hl-node", "hl-visor"}

// processPollInterval is short because operators want quick detection of
// hl-node crashes — restart-loop monitoring is one of the canonical
// reasons to run this exporter.
const processPollInterval = 15 * time.Second

// StartProcessMonitor publishes resource usage and liveness for each
// known hl- process. The implementation is Linux-only; on darwin/macOS
// (or any OS without /proc) the monitor logs once and idles.
func StartProcessMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	if !procFSAvailable() {
		logger.InfoComponent("process",
			"process monitor disabled: /proc not available on this OS")
		<-ctx.Done()
		return
	}

	logger.InfoComponent("process", "watching %v via /proc", processNames)

	ticker := time.NewTicker(processPollInterval)
	defer ticker.Stop()

	tickProcesses()
	metrics.MarkMonitorTick("process")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickProcesses()
			metrics.MarkMonitorTick("process")
		}
	}
}

func tickProcesses() {
	for _, name := range processNames {
		info, ok := findProcess(name)
		if !ok {
			metrics.HLNodeProcessUp.WithLabelValues(name).Set(0)
			// Reset companion gauges to zero so a crash is visible on the
			// dashboard rather than showing the last live value forever.
			metrics.HLNodeProcessStartTimeSeconds.WithLabelValues(name).Set(0)
			metrics.HLNodeProcessCPUSecondsTotal.WithLabelValues(name).Set(0)
			metrics.HLNodeProcessRSSBytes.WithLabelValues(name).Set(0)
			metrics.HLNodeProcessVirtBytes.WithLabelValues(name).Set(0)
			metrics.HLNodeProcessThreads.WithLabelValues(name).Set(0)
			metrics.HLNodeProcessOpenFDs.WithLabelValues(name).Set(0)
			continue
		}
		metrics.HLNodeProcessUp.WithLabelValues(name).Set(1)
		metrics.HLNodeProcessStartTimeSeconds.WithLabelValues(name).Set(float64(info.StartTimeUnix))
		metrics.HLNodeProcessCPUSecondsTotal.WithLabelValues(name).Set(info.CPUSeconds)
		metrics.HLNodeProcessRSSBytes.WithLabelValues(name).Set(float64(info.RSSBytes))
		metrics.HLNodeProcessVirtBytes.WithLabelValues(name).Set(float64(info.VirtBytes))
		metrics.HLNodeProcessThreads.WithLabelValues(name).Set(float64(info.Threads))
		metrics.HLNodeProcessOpenFDs.WithLabelValues(name).Set(float64(info.OpenFDs))
	}
}

// processInfo is the OS-independent view the publisher consumes.
type processInfo struct {
	PID           int
	StartTimeUnix int64
	CPUSeconds    float64
	RSSBytes      int64
	VirtBytes     int64
	Threads       int64
	OpenFDs       int64
}
