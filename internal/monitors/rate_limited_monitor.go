package monitors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	rateLimitedPollInterval = 60 * time.Second
	rateLimitedRecentWindow = 120 * time.Second
)

var rateLimitedStreams = []string{"abci_stream", "gossip_rpc_blocks", "gossip_rpc_requests"}

var rateLimitedSourceIDs = map[string]metrics.SourceID{
	"abci_stream":         metrics.SourceRateLimitABCI,
	"gossip_rpc_blocks":   metrics.SourceRateLimitBlocks,
	"gossip_rpc_requests": metrics.SourceRateLimitRequests,
}

type rateLimitedSnapshot struct {
	retained     int
	recent       int
	lastNonempty time.Time
}

type rateLimitedScanError struct {
	stage string
	err   error
}

func (e *rateLimitedScanError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *rateLimitedScanError) Unwrap() error { return e.err }

func StartRateLimitedMonitor(ctx context.Context, cfg config.Config) {
	root := filepath.Join(cfg.NodeHome, "data", "rate_limited_ips")
	for _, source := range rateLimitedSourceIDs {
		metrics.RegisterSource(source, true)
	}
	logger.InfoComponent("rate_limited", "watching %s (late discovery enabled)", root)

	lastNonempty := make(map[string]time.Time, len(rateLimitedStreams))
	ticker := time.NewTicker(rateLimitedPollInterval)
	defer ticker.Stop()
	tickRateLimitedAt(root, time.Now(), lastNonempty)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickRateLimitedAt(root, time.Now(), lastNonempty)
		}
	}
}

func tickRateLimitedAt(root string, now time.Time, lastNonempty map[string]time.Time) {
	metrics.MarkMonitorAttempt("rate_limited")
	committed := 0
	for _, stream := range rateLimitedStreams {
		source := rateLimitedSourceIDs[stream]
		metrics.MarkSourceAttempt(source)
		snapshot, err := scanRateLimitedStream(filepath.Join(root, stream, "hourly"), now)
		if err != nil {
			markRateLimitedScanFailure(stream, source, err)
			continue
		}
		if snapshot.lastNonempty.After(lastNonempty[stream]) {
			lastNonempty[stream] = snapshot.lastNonempty
		}
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.HLNodeRateLimitedNonemptyFilesLatestDate.WithLabelValues(stream).Set(float64(snapshot.retained))
			metrics.HLNodeRateLimitedRecentFiles.WithLabelValues(stream).Set(float64(snapshot.recent))
			lastUpdate := 0.0
			if !lastNonempty[stream].IsZero() {
				lastUpdate = float64(lastNonempty[stream].Unix())
			}
			metrics.HLNodeRateLimitedLastNonemptyUpdateTimestampSeconds.WithLabelValues(stream).Set(lastUpdate)
			metrics.HLNodeRateLimitedSourceUp.WithLabelValues(stream).Set(1)
			metrics.HLNodeRateLimitedLastSuccessTimestampSeconds.WithLabelValues(stream).Set(float64(now.Unix()))
			metrics.HLNodeRateLimitedFiles.WithLabelValues(stream).Set(float64(snapshot.retained)) // one-release alias
			metrics.MarkSourceValidObservation(source, time.Time{})
			metrics.MarkSourcePublication(source)
		})
		committed++
	}
	if committed > 0 {
		metrics.MarkMonitorValidObservation("rate_limited")
		metrics.MarkMonitorPublication("rate_limited")
	}
}

func markRateLimitedScanFailure(stream string, source metrics.SourceID, err error) {
	stage := "root"
	var scanErr *rateLimitedScanError
	if errors.As(err, &scanErr) {
		stage = scanErr.stage
	}
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLNodeRateLimitedSourceUp.WithLabelValues(stream).Set(0)
		metrics.HLNodeRateLimitedReadErrorsTotal.WithLabelValues(stream, stage).Inc()
		if stage == "root" && errors.Is(err, os.ErrNotExist) {
			// A missing fixed root is confirmed absence. Races below that
			// root are read failures and must not erase source presence.
			metrics.MarkSourceAbsent(source)
		} else {
			metrics.MarkSourceError(source, metrics.SourceFailureRead)
		}
		metrics.IncMonitorError("rate_limited")
	})
}

func scanRateLimitedStream(hourlyRoot string, now time.Time) (rateLimitedSnapshot, error) {
	entries, err := os.ReadDir(hourlyRoot)
	if err != nil {
		return rateLimitedSnapshot{}, &rateLimitedScanError{stage: "root", err: err}
	}
	dates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			dates = append(dates, entry.Name())
		}
	}
	if len(dates) == 0 {
		return rateLimitedSnapshot{}, nil
	}
	sort.Strings(dates)
	var snapshot rateLimitedSnapshot
	latestDate := dates[len(dates)-1]
	for _, date := range dates {
		files, err := os.ReadDir(filepath.Join(hourlyRoot, date))
		if err != nil {
			return rateLimitedSnapshot{}, &rateLimitedScanError{stage: "date", err: err}
		}
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return rateLimitedSnapshot{}, &rateLimitedScanError{stage: "fileinfo", err: err}
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 {
				continue
			}
			if date == latestDate {
				snapshot.retained++
			}
			age := now.Sub(info.ModTime())
			if age >= 0 && age <= rateLimitedRecentWindow {
				snapshot.recent++
			}
			// A future filesystem timestamp is not evidence of a future
			// limiter event. Keep the retained file count, but exclude it from
			// every time-derived gauge.
			if !info.ModTime().After(now) && info.ModTime().After(snapshot.lastNonempty) {
				snapshot.lastNonempty = info.ModTime()
			}
		}
	}
	return snapshot, nil
}
