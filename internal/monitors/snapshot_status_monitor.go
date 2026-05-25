package monitors

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// snapshotStatusPollInterval matches the underlying ~11-min snapshot
// cadence at 30s tick; cheap enough to leave at this rate.
const snapshotStatusPollInterval = 30 * time.Second

// StartSnapshotStatusMonitor watches
// $NODE_HOME/data/periodic_abci_state_statuses/<YYYYMMDD>/<height>.
//
// Each entry is a zero-byte file written by hl-node AFTER the
// corresponding .rmp snapshot completes successfully. The filename is
// the block height; the file's mtime is when the snapshot finished. The
// status sentinel outlives the .rmp itself (we observe ~58 statuses
// retained vs ~37 .rmps), so this directory is the right source for
// "when did the last snapshot succeed".
//
// Operator alert: hl_node_snapshot_last_age_seconds > 15m means the
// snapshot pipeline has stalled.
func StartSnapshotStatusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "periodic_abci_state_statuses")

	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("snapshot_status",
			"periodic_abci_state_statuses directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("snapshot_status", "watching %s", root)

	ticker := time.NewTicker(snapshotStatusPollInterval)
	defer ticker.Stop()

	tickSnapshotStatus(root)
	metrics.MarkMonitorTick("snapshot_status")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickSnapshotStatus(root)
			metrics.MarkMonitorTick("snapshot_status")
		}
	}
}

func tickSnapshotStatus(root string) {
	// Latest date directory.
	dateEntries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var latestDate string
	for _, e := range dateEntries {
		if !e.IsDir() {
			continue
		}
		if e.Name() > latestDate {
			latestDate = e.Name()
		}
	}
	if latestDate == "" {
		return
	}
	dateDir := filepath.Join(root, latestDate)

	hgtEntries, err := os.ReadDir(dateDir)
	if err != nil {
		return
	}
	var maxHeight int64
	var maxMtime time.Time
	count := 0
	for _, e := range hgtEntries {
		h, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue
		}
		count++
		if h > maxHeight {
			maxHeight = h
		}
		if info, err := e.Info(); err == nil {
			if mt := info.ModTime(); mt.After(maxMtime) {
				maxMtime = mt
			}
		}
	}
	if maxHeight > 0 {
		metrics.HLNodeSnapshotLastHeight.Set(float64(maxHeight))
	}
	if !maxMtime.IsZero() {
		metrics.HLNodeSnapshotLastAgeSeconds.Set(time.Since(maxMtime).Seconds())
	}
	metrics.HLNodeSnapshotKnown.Set(float64(count))
}
