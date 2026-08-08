package monitors

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const logLinesPollInterval = 60 * time.Second

// logTargets enumerates the (stream, level) tuples we expose. The node
// rotates each daily. The 'visor' stream is validator-only and ships
// hl-visor's own warn/error lines (e.g. failed binary downloads).
var logTargets = []struct {
	stream string
	level  string
}{
	{"infra", "error"},
	{"infra", "warn"},
	{"trade", "error"},
	{"trade", "warn"},
	{"visor", "error"},
	{"visor", "warn"},
}

// StartLogLinesMonitor counts lines and bytes in
// $NODE_HOME/data/log/<stream>/<level>/<YYYYMMDD>. Both files are
// empty on a healthy node; any non-zero line count is operator-relevant.
//
// Modeled as a gauge that resets at the day boundary because the day-file
// rotation cleanly zeroes the series; treating it as a counter would
// look like a regression at midnight UTC.
func StartLogLinesMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceLogLines, true)
	root := filepath.Join(cfg.NodeHome, "data", "log")
	logger.InfoComponent("log_lines", "watching %s with late-source discovery", root)

	ticker := time.NewTicker(logLinesPollInterval)
	defer ticker.Stop()

	tickLogLines(root)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickLogLines(root)
		}
	}
}

type logLineSnapshot struct {
	lines int64
	bytes int64
}

func tickLogLines(root string) bool {
	metrics.MarkMonitorAttempt("log_lines")
	metrics.MarkSourceAttempt(metrics.SourceLogLines)
	if info, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceLogLines)
		} else {
			metrics.MarkSourceError(metrics.SourceLogLines, metrics.SourceFailureStat)
		}
		return false
	} else if !info.IsDir() {
		metrics.MarkSourceError(metrics.SourceLogLines, metrics.SourceFailureSchema)
		return false
	}

	snapshot := make([]logLineSnapshot, len(logTargets))
	for i, target := range logTargets {
		dir := filepath.Join(root, target.stream, target.level)
		path, err := latestDateFile(dir)
		if err != nil {
			// Day file rotates at UTC midnight — between rotation and the
			// node creating the new file, the dir may briefly look empty.
			// A confirmed missing fixed target is a valid zero; other read
			// failures reject the complete generation.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			metrics.MarkSourceError(metrics.SourceLogLines, metrics.SourceFailureDiscovery)
			return false
		}
		lines, size, ok := countLinesAndBytes(path)
		if !ok {
			metrics.MarkSourceError(metrics.SourceLogLines, metrics.SourceFailureRead)
			return false
		}
		snapshot[i] = logLineSnapshot{lines: lines, bytes: size}
	}
	metrics.WithPrometheusSnapshotUpdate(func() {
		for i, target := range logTargets {
			metrics.HLNodeLogLinesTotal.WithLabelValues(target.stream, target.level).Set(float64(snapshot[i].lines))
			metrics.HLNodeLogBytes.WithLabelValues(target.stream, target.level).Set(float64(snapshot[i].bytes))
		}
		metrics.MarkSourceValidObservation(metrics.SourceLogLines, time.Time{})
		metrics.MarkSourcePublication(metrics.SourceLogLines)
		metrics.MarkMonitorValidObservation("log_lines")
		metrics.MarkMonitorPublication("log_lines")
	})
	return true
}

// countLinesAndBytes returns (number of \n-terminated lines, byte size).
// Reads the file in 64KiB chunks so a multi-MB log doesn't allocate the
// full body. Returns ok=false on any IO error.
func countLinesAndBytes(path string) (int64, int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, 0, false
	}
	if info.Size() == 0 {
		return 0, 0, true
	}

	buf := make([]byte, 64*1024)
	var lines int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			lines += int64(bytes.Count(buf[:n], []byte{'\n'}))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, false
		}
	}
	return lines, info.Size(), true
}
