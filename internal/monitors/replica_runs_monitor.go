package monitors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// These directories change only on hl-node startup, so a slow poll is enough.
const replicaRunsPollInterval = 60 * time.Second

var errInvalidReplicaRunEntry = errors.New("invalid replica run entry")

type replicaRunsSnapshot struct {
	Retained       int
	LatestStart    time.Time
	LatestActivity time.Time
}

// StartReplicaRunsMonitor publishes the retained replica_cmds session window.
// A run directory's RFC3339 name is its immutable start. Directory mtime is
// exposed separately as filesystem activity because adding a UTC date child
// retouches it. Neither value proves an end time or a run duration.
func StartReplicaRunsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "replica_cmds")
	metrics.RegisterSource(metrics.SourceReplicaRuns, true)
	logger.InfoComponent("replica_runs", "polling %s", root)

	ticker := time.NewTicker(replicaRunsPollInterval)
	defer ticker.Stop()

	tickReplicaRuns(root)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickReplicaRuns(root)
		}
	}
}

func tickReplicaRuns(root string) bool {
	metrics.MarkMonitorAttempt("replica_runs")
	metrics.MarkSourceAttempt(metrics.SourceReplicaRuns)
	snapshot, err := scanReplicaRuns(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceReplicaRuns)
			metrics.HLNodeObservedRunsTotal.Set(0)
			metrics.HLNodeObservedRunStartSeconds.Set(0)
			metrics.HLNodeObservedRunLastActivitySeconds.Set(0)
			return false
		}
		stage := metrics.SourceFailureRead
		if errors.Is(err, errInvalidReplicaRunEntry) {
			stage = metrics.SourceFailureSchema
		}
		metrics.MarkSourceError(metrics.SourceReplicaRuns, stage)
		metrics.IncMonitorError("replica_runs")
		return false
	}

	metrics.HLNodeObservedRunsTotal.Set(float64(snapshot.Retained))
	metrics.HLNodeObservedRunStartSeconds.Set(optionalUnix(snapshot.LatestStart))
	metrics.HLNodeObservedRunLastActivitySeconds.Set(optionalUnix(snapshot.LatestActivity))
	metrics.MarkSourceValidObservation(metrics.SourceReplicaRuns, snapshot.LatestStart)
	metrics.MarkSourcePublication(metrics.SourceReplicaRuns)
	metrics.MarkMonitorValidObservation("replica_runs")
	metrics.MarkMonitorPublication("replica_runs")
	return true
}

func scanReplicaRuns(root string) (replicaRunsSnapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return replicaRunsSnapshot{}, err
	}
	var snapshot replicaRunsSnapshot
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		start, err := time.Parse(time.RFC3339Nano, entry.Name())
		if err != nil {
			return replicaRunsSnapshot{}, fmt.Errorf("%w: %q", errInvalidReplicaRunEntry, entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return replicaRunsSnapshot{}, err
		}
		snapshot.Retained++
		if start.After(snapshot.LatestStart) {
			snapshot.LatestStart = start
		}
		if info.ModTime().After(snapshot.LatestActivity) {
			snapshot.LatestActivity = info.ModTime()
		}
	}
	return snapshot, nil
}

func optionalUnix(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.Unix())
}
