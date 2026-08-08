package monitors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const replayPollInterval = 60 * time.Second

var errInvalidReplayEntry = errors.New("invalid replay entry")

type replaySnapshot struct {
	Retained       int
	LatestHeight   int64
	LatestStart    time.Time
	LatestActivity time.Time
}

// StartReplayMonitor watches retained <height>_<RFC3339-start> markers. The
// encoded timestamp is immutable and the height is the recovery start floor.
// Directory mtime is only activity; no observed marker proves an end/duration.
func StartReplayMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "replay")
	metrics.RegisterSource(metrics.SourceReplay, true)
	logger.InfoComponent("replay", "polling %s", root)

	ticker := time.NewTicker(replayPollInterval)
	defer ticker.Stop()

	tickReplay(root)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickReplay(root)
		}
	}
}

func tickReplay(root string) bool {
	metrics.MarkMonitorAttempt("replay")
	metrics.MarkSourceAttempt(metrics.SourceReplay)
	snapshot, err := scanReplay(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			withdrawReplaySnapshot()
			return false
		}
		stage := metrics.SourceFailureRead
		if errors.Is(err, errInvalidReplayEntry) {
			stage = metrics.SourceFailureSchema
		}
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourceReplay, stage)
			metrics.IncMonitorError("replay")
		})
		return false
	}

	commitReplaySnapshot(snapshot)
	return true
}

func withdrawReplaySnapshot() {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLNodeReplayEventsTotal.DeleteLabelValues()
		metrics.HLNodeReplayLastSeconds.DeleteLabelValues()
		metrics.HLNodeReplayLastHeight.DeleteLabelValues()
		metrics.HLNodeReplayLastActivitySeconds.DeleteLabelValues()
		metrics.MarkSourceAbsent(metrics.SourceReplay)
		metrics.MarkSourcePublication(metrics.SourceReplay)
		metrics.MarkMonitorPublication("replay")
	})
}

func commitReplaySnapshot(snapshot replaySnapshot) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLNodeReplayEventsTotal.WithLabelValues().Set(float64(snapshot.Retained))
		metrics.HLNodeReplayLastSeconds.WithLabelValues().Set(optionalUnix(snapshot.LatestStart))
		metrics.HLNodeReplayLastHeight.WithLabelValues().Set(float64(snapshot.LatestHeight))
		metrics.HLNodeReplayLastActivitySeconds.WithLabelValues().Set(optionalUnix(snapshot.LatestActivity))
		metrics.MarkSourceValidObservation(metrics.SourceReplay, snapshot.LatestStart)
		metrics.MarkSourcePublication(metrics.SourceReplay)
		metrics.MarkMonitorValidObservation("replay")
		metrics.MarkMonitorPublication("replay")
	})
}

func scanReplay(root string) (replaySnapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return replaySnapshot{}, err
	}
	var snapshot replaySnapshot
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		height, start, err := parseReplayEntryName(entry.Name())
		if err != nil {
			return replaySnapshot{}, err
		}
		info, err := entry.Info()
		if err != nil {
			return replaySnapshot{}, err
		}
		snapshot.Retained++
		if start.After(snapshot.LatestStart) {
			snapshot.LatestStart = start
			snapshot.LatestHeight = height
		}
		if info.ModTime().After(snapshot.LatestActivity) {
			snapshot.LatestActivity = info.ModTime()
		}
	}
	return snapshot, nil
}

func parseReplayEntryName(name string) (int64, time.Time, error) {
	separator := strings.IndexByte(name, '_')
	if separator <= 0 || separator == len(name)-1 {
		return 0, time.Time{}, fmt.Errorf("%w: %q", errInvalidReplayEntry, name)
	}
	height, err := strconv.ParseInt(name[:separator], 10, 64)
	if err != nil || height <= 0 {
		return 0, time.Time{}, fmt.Errorf("%w: invalid height", errInvalidReplayEntry)
	}
	start, err := time.Parse(time.RFC3339Nano, name[separator+1:])
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("%w: invalid timestamp", errInvalidReplayEntry)
	}
	return height, start, nil
}
