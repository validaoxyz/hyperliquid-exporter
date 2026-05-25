package monitors

import (
	"bytes"
	"context"
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
// rotates each daily.
var logTargets = []struct {
	stream string
	level  string
}{
	{"infra", "error"},
	{"infra", "warn"},
	{"trade", "error"},
	{"trade", "warn"},
}

// StartLogLinesMonitor counts lines and bytes in
// $NODE_HOME/data/log/<stream>/<level>/<YYYYMMDD>. Both files are
// empty on a healthy node; any non-zero line count is operator-relevant.
//
// Modeled as a gauge that resets at the day boundary because the day-file
// rotation cleanly zeroes the series; treating it as a counter would
// look like a regression at midnight UTC.
func StartLogLinesMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "log")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("log_lines",
			"log directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("log_lines", "watching %s", root)

	ticker := time.NewTicker(logLinesPollInterval)
	defer ticker.Stop()

	tickLogLines(root)
	metrics.MarkMonitorTick("log_lines")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickLogLines(root)
			metrics.MarkMonitorTick("log_lines")
		}
	}
}

func tickLogLines(root string) {
	for _, t := range logTargets {
		dir := filepath.Join(root, t.stream, t.level)
		path, err := latestDateFile(dir)
		if err != nil {
			// Day file rotates at UTC midnight — between rotation and the
			// node creating the new file, the dir may briefly look empty.
			// Report zero rather than freeze the gauges at the previous
			// day's values.
			metrics.HLNodeLogLinesTotal.WithLabelValues(t.stream, t.level).Set(0)
			metrics.HLNodeLogBytes.WithLabelValues(t.stream, t.level).Set(0)
			continue
		}
		lines, size, ok := countLinesAndBytes(path)
		if !ok {
			continue
		}
		metrics.HLNodeLogLinesTotal.WithLabelValues(t.stream, t.level).Set(float64(lines))
		metrics.HLNodeLogBytes.WithLabelValues(t.stream, t.level).Set(float64(size))
	}
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
