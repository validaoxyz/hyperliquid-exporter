package monitors

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// replicaRunsPollInterval — these dirs change only on hl-node startup,
// so a slow tick is fine.
const replicaRunsPollInterval = 60 * time.Second

// StartReplicaRunsMonitor publishes filesystem-observable hl-node run
// tracking. The data/replica_cmds/ directory contains one
// `<run_timestamp>/` subdirectory per hl-node startup; counting them
// gives the lifetime run count, and the newest mtime gives the current
// run's start time.
//
// This is a deliberate redundancy with the /proc-based process monitor:
//
//   - process_monitor reads /proc/PID/stat — most accurate, Linux-only,
//     fails in containerized deploys where /proc isn't visible.
//   - replica_runs reads the data directory hl-node writes to anyway —
//     less precise (5s mkdir granularity, not the actual process clock)
//     but works in any environment that can read NODE_HOME.
//
// Both publish at the same logical metric tier so dashboards can fall
// back gracefully when one source is unavailable.
func StartReplicaRunsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "replica_cmds")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("replica_runs",
			"replica_cmds directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("replica_runs", "watching %s", root)

	ticker := time.NewTicker(replicaRunsPollInterval)
	defer ticker.Stop()

	tickReplicaRuns(root)
	metrics.MarkMonitorTick("replica_runs")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickReplicaRuns(root)
			metrics.MarkMonitorTick("replica_runs")
		}
	}
}

func tickReplicaRuns(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var count int
	var newestMtime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		count++
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mt := info.ModTime(); mt.After(newestMtime) {
			newestMtime = mt
		}
	}
	metrics.HLNodeObservedRunsTotal.Set(float64(count))
	if !newestMtime.IsZero() {
		metrics.HLNodeObservedRunStartSeconds.Set(float64(newestMtime.Unix()))
	}
}
