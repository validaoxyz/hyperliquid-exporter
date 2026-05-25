package monitors

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// diskPollInterval is intentionally slow: walking NODE_HOME can be many
// thousands of inodes and a full disk doesn't change in seconds. statfs
// is cheap but the recursive size walk dominates.
const diskPollInterval = 60 * time.Second

type fsStats struct {
	Bavail uint64
	Blocks uint64
	Bsize  uint64
}

// trackedSubdirs is the allowlist of subpaths whose recursive byte usage
// the monitor publishes individually. These are the directories most
// likely to consume runaway disk on a Hyperliquid node. The list
// includes both broad rollups (hyperliquid_data, data/replica_cmds)
// and per-RocksDB subpaths so operators can tell *which* DB is
// bloating without ssh'ing in.
var trackedSubdirs = []string{
	"data/replica_cmds",
	"data/evm_block_and_receipts",
	"data/node_logs",
	"data/latency_buckets",
	"data/latency_summaries",
	"data/periodic_abci_states",
	"data/visor_abci_states",
	"data/tcp_traffic",
	"data/dhs",
	"hyperliquid_data",
	"hyperliquid_data/db_hub/Evm",
	"hyperliquid_data/db_hub/Exchange",
	"hyperliquid_data/db_hub/Rpc",
	"hyperliquid_data/evm_db_hub_fast",
	"hyperliquid_data/evm_db_hub_slow",
	"hyperliquid_data/evm_db_hub_slow/checkpoint",
	"tmp",
}

// StartDiskMonitor walks NODE_HOME once a minute and publishes its size
// alongside the host filesystem's free/total. Operators want to know
// "how much room do I have before this node breaks" without ssh'ing in;
// disk filling is one of the most common silent failure modes for
// blockchain nodes.
func StartDiskMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	if _, err := os.Stat(cfg.NodeHome); err != nil {
		logger.WarningComponent("disk",
			"NODE_HOME %s not accessible, disk monitor idle: %v", cfg.NodeHome, err)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("disk", "starting disk monitor for %s (every %s)",
		cfg.NodeHome, diskPollInterval)

	ticker := time.NewTicker(diskPollInterval)
	defer ticker.Stop()

	tickDisk(cfg.NodeHome)
	metrics.MarkMonitorTick("disk")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickDisk(cfg.NodeHome)
			metrics.MarkMonitorTick("disk")
		}
	}
}

func tickDisk(nodeHome string) {
	// Filesystem-level numbers come from statfs and don't require walking
	// the tree, so do them first and unconditionally — even if the tree
	// walk later fails or is slow, the operator still gets free/total.
	if stat, err := statfs(nodeHome); err == nil {
		metrics.HLNodeDiskFreeBytes.Set(float64(stat.Bavail) * float64(stat.Bsize))
		metrics.HLNodeDiskTotalBytes.Set(float64(stat.Blocks) * float64(stat.Bsize))
	} else {
		logger.DebugComponent("disk", "statfs failed: %v", err)
	}

	total := dirSize(nodeHome)
	metrics.HLNodeDiskUsedBytes.Set(float64(total))

	for _, sub := range trackedSubdirs {
		path := filepath.Join(nodeHome, sub)
		size := dirSize(path)
		metrics.HLNodeDiskSubdirBytes.WithLabelValues(sub).Set(float64(size))
	}
}

// dirSize sums the on-disk sizes of all regular files under root. Returns 0
// on any error (missing directory, permission denied, etc) — callers treat
// "doesn't exist" the same as "0 bytes" which is appropriate for
// the tracked-subdirs allowlist.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
