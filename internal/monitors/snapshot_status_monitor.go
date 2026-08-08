package monitors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const snapshotStatusPollInterval = 30 * time.Second

var errInvalidSnapshotStatusEntry = errors.New("invalid snapshot status entry")

type snapshotStatusSnapshot struct {
	DateDirs       int
	Known          int
	LatestHeight   int64
	LatestComplete time.Time
}

// StartSnapshotStatusMonitor polls the fixed status root even when it is
// absent at startup. hl-node writes a zero-byte <date>/<height> sentinel after
// a height-driven snapshot completes. At most the two newest date directories
// form the retained scan window; their count is not cadence or headroom.
func StartSnapshotStatusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "periodic_abci_state_statuses")
	metrics.RegisterSource(metrics.SourceSnapshotStatus, true)
	logger.InfoComponent("snapshot_status", "polling %s", root)

	ticker := time.NewTicker(snapshotStatusPollInterval)
	defer ticker.Stop()

	tickSnapshotStatus(root)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickSnapshotStatus(root)
		}
	}
}

func tickSnapshotStatus(root string) bool {
	metrics.MarkMonitorAttempt("snapshot_status")
	metrics.MarkSourceAttempt(metrics.SourceSnapshotStatus)
	snapshot, err := scanSnapshotStatus(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			withdrawSnapshotStatus()
			return false
		}
		stage := metrics.SourceFailureRead
		if errors.Is(err, errInvalidSnapshotStatusEntry) {
			stage = metrics.SourceFailureSchema
		}
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourceSnapshotStatus, stage)
			metrics.IncMonitorError("snapshot_status")
		})
		return false
	}

	commitSnapshotStatus(snapshot, time.Now(), latestVisorHeight())
	return true
}

func withdrawSnapshotStatus() {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLNodeSnapshotKnown.DeleteLabelValues()
		metrics.HLNodeSnapshotLastHeight.DeleteLabelValues()
		metrics.HLNodeSnapshotLastAgeSeconds.DeleteLabelValues()
		metrics.HLNodeSnapshotHeightLagAvailable.DeleteLabelValues()
		metrics.HLNodeSnapshotHeightLagBlocks.DeleteLabelValues()
		metrics.MarkSourceAbsent(metrics.SourceSnapshotStatus)
		metrics.MarkSourcePublication(metrics.SourceSnapshotStatus)
		metrics.MarkMonitorPublication("snapshot_status")
	})
}

func commitSnapshotStatus(snapshot snapshotStatusSnapshot, now time.Time, currentHeight int64) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		publishSnapshotStatusValues(snapshot, now, currentHeight)
		metrics.MarkSourceValidObservation(metrics.SourceSnapshotStatus, snapshot.LatestComplete)
		metrics.MarkSourcePublication(metrics.SourceSnapshotStatus)
		metrics.MarkMonitorValidObservation("snapshot_status")
		metrics.MarkMonitorPublication("snapshot_status")
	})
}

func publishSnapshotStatusValues(snapshot snapshotStatusSnapshot, now time.Time, currentHeight int64) {
	metrics.HLNodeSnapshotKnown.WithLabelValues().Set(float64(snapshot.Known))
	metrics.HLNodeSnapshotLastHeight.WithLabelValues().Set(float64(snapshot.LatestHeight))
	if snapshot.LatestComplete.IsZero() {
		metrics.HLNodeSnapshotLastAgeSeconds.WithLabelValues().Set(0)
		metrics.HLNodeSnapshotHeightLagAvailable.WithLabelValues().Set(0)
		metrics.HLNodeSnapshotHeightLagBlocks.DeleteLabelValues()
		return
	}
	age := now.Sub(snapshot.LatestComplete).Seconds()
	if age < 0 {
		age = 0
	}
	metrics.HLNodeSnapshotLastAgeSeconds.WithLabelValues().Set(age)
	if currentHeight >= snapshot.LatestHeight && snapshot.LatestHeight > 0 {
		metrics.HLNodeSnapshotHeightLagBlocks.WithLabelValues().Set(float64(currentHeight - snapshot.LatestHeight))
		metrics.HLNodeSnapshotHeightLagAvailable.WithLabelValues().Set(1)
		return
	}
	metrics.HLNodeSnapshotHeightLagBlocks.DeleteLabelValues()
	metrics.HLNodeSnapshotHeightLagAvailable.WithLabelValues().Set(0)
}

func scanSnapshotStatus(root string) (snapshotStatusSnapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return snapshotStatusSnapshot{}, err
	}
	dates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		parsed, err := time.Parse("20060102", entry.Name())
		if err != nil || parsed.Format("20060102") != entry.Name() {
			return snapshotStatusSnapshot{}, fmt.Errorf("%w: invalid date directory", errInvalidSnapshotStatusEntry)
		}
		dates = append(dates, entry.Name())
	}
	sort.Strings(dates)
	if len(dates) > 2 {
		dates = dates[len(dates)-2:]
	}

	snapshot := snapshotStatusSnapshot{DateDirs: len(dates)}
	for _, date := range dates {
		heightEntries, err := os.ReadDir(filepath.Join(root, date))
		if err != nil {
			return snapshotStatusSnapshot{}, err
		}
		for _, entry := range heightEntries {
			if entry.IsDir() {
				return snapshotStatusSnapshot{}, fmt.Errorf("%w: nested directory", errInvalidSnapshotStatusEntry)
			}
			height, err := strconv.ParseInt(entry.Name(), 10, 64)
			if err != nil || height <= 0 {
				return snapshotStatusSnapshot{}, fmt.Errorf("%w: invalid height", errInvalidSnapshotStatusEntry)
			}
			info, err := entry.Info()
			if err != nil {
				return snapshotStatusSnapshot{}, err
			}
			if info.Size() != 0 {
				return snapshotStatusSnapshot{}, fmt.Errorf("%w: nonempty sentinel", errInvalidSnapshotStatusEntry)
			}
			snapshot.Known++
			if height > snapshot.LatestHeight || (height == snapshot.LatestHeight && info.ModTime().After(snapshot.LatestComplete)) {
				snapshot.LatestHeight = height
				snapshot.LatestComplete = info.ModTime()
			}
		}
	}
	return snapshot, nil
}
