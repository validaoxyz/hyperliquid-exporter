package monitors

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// replayPollInterval — replay events are rare (one or two per week is
// typical even on a busy validator). 60s is plenty.
const replayPollInterval = 60 * time.Second

// StartReplayMonitor counts subdirectories under
// $NODE_HOME/data/node_logs/replay/. Each entry is named
// "<height>_<iso-restart>" and is created by hl-node when it enters
// replay mode (recovering from a checkpoint). The dir contents are
// usually empty — the existence and name of the entry are the signal.
//
// Operator semantics: a healthy validator has very few replay events
// per week. Sustained growth = repeated recoveries, often correlated
// with network instability or hl-visor restarts.
//
// Validator-only (the replay dir exists on validators that have ever
// recovered; absent on greenfield non-validator nodes).
func StartReplayMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "replay")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("replay",
			"replay directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("replay", "watching %s", root)

	ticker := time.NewTicker(replayPollInterval)
	defer ticker.Stop()

	tickReplay(root)
	metrics.MarkMonitorTick("replay")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickReplay(root)
			metrics.MarkMonitorTick("replay")
		}
	}
}

func tickReplay(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var count int64
	var newestMtime time.Time
	var newestHeight int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		count++
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if mt.After(newestMtime) {
			newestMtime = mt
			// parse height from "<height>_<iso>"
			name := e.Name()
			if idx := strings.IndexByte(name, '_'); idx > 0 {
				if h, err := strconv.ParseInt(name[:idx], 10, 64); err == nil {
					newestHeight = h
				}
			}
		}
	}
	metrics.HLNodeReplayEventsTotal.Set(float64(count))
	if !newestMtime.IsZero() {
		metrics.HLNodeReplayLastSeconds.Set(float64(newestMtime.Unix()))
	}
	if newestHeight > 0 {
		metrics.HLNodeReplayLastHeight.Set(float64(newestHeight))
	}
}
