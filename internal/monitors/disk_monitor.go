package monitors

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// diskPollInterval is intentionally slow: walking NODE_HOME visits hundreds
// of thousands of inodes on a long-lived node and a full disk doesn't change
// in seconds. statfs is cheap but the recursive size walk dominates.
const diskPollInterval = 120 * time.Second

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

	total, bySub := walkSizes(nodeHome, trackedSubdirs)
	metrics.HLNodeDiskUsedBytes.Set(float64(total))

	for _, sub := range trackedSubdirs {
		metrics.HLNodeDiskSubdirBytes.WithLabelValues(sub).Set(float64(bySub[sub]))
	}
}

// walkSizes walks nodeHome once, attributing every regular file's size to
// the grand total and to each tracked subdir prefix it lives under.
// Prefixes nest (hyperliquid_data contains hyperliquid_data/db_hub/Evm), so
// a file adds to every matching bucket. The single pass replaces the
// previous walk-per-subdir approach, which re-traversed the biggest trees
// up to three times per tick. Unreadable entries are skipped silently and
// missing tracked dirs simply report 0.
func walkSizes(nodeHome string, subs []string) (int64, map[string]int64) {
	bySub := make(map[string]int64, len(subs))
	prefixes := make([]string, len(subs))
	for i, sub := range subs {
		prefixes[i] = filepath.Join(nodeHome, sub) + string(filepath.Separator)
	}

	var total int64
	_ = filepath.WalkDir(nodeHome, func(path string, d fs.DirEntry, err error) error {
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
		size := info.Size()
		total += size
		for i, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				bySub[subs[i]] += size
			}
		}
		return nil
	})
	return total, bySub
}
