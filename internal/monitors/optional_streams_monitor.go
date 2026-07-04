package monitors

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const optionalStreamsPollInterval = 60 * time.Second

// optionalStreamDirs is the allowlist of opt-in hl-node data streams whose
// freshness we track. Operators enable these with node flags
// (--write-fills, --write-misc-events, --write-system-and-core-writer-actions,
// TWAP streaming) specifically to feed downstream consumers; the stream
// content is bulk order-flow/ledger data and out of metric scope, but a
// stream that silently stops writing breaks those consumers, and nothing
// else surfaces that.
var optionalStreamDirs = []string{
	"node_fills_streaming",
	"node_twap_statuses_streaming",
	"misc_events",
	"system_and_core_writer_actions",
}

// StartOptionalStreamsMonitor publishes the age of the newest file in each
// present opt-in stream under $NODE_HOME/data/<stream>/hourly/. Streams
// whose directory is absent produce no series.
func StartOptionalStreamsMonitor(ctx context.Context, cfg config.Config) {
	dataRoot := filepath.Join(cfg.NodeHome, "data")

	present := []string{}
	for _, stream := range optionalStreamDirs {
		if _, err := os.Stat(filepath.Join(dataRoot, stream)); err == nil {
			present = append(present, stream)
		}
	}
	if len(present) == 0 {
		logger.InfoComponent("optional_streams", "no opt-in data streams present; monitor idle")
		<-ctx.Done()
		return
	}

	logger.InfoComponent("optional_streams", "watching opt-in streams: %v", present)

	ticker := time.NewTicker(optionalStreamsPollInterval)
	defer ticker.Stop()

	tickOptionalStreams(dataRoot)
	metrics.MarkMonitorTick("optional_streams")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickOptionalStreams(dataRoot)
			metrics.MarkMonitorTick("optional_streams")
		}
	}
}

func tickOptionalStreams(dataRoot string) {
	for _, stream := range optionalStreamDirs {
		root := filepath.Join(dataRoot, stream, "hourly")
		path, err := latestHourlyFile(root)
		if err != nil {
			// stream can appear after startup (operator flips the node
			// flag); keep checking every tick, publish nothing until then
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		metrics.HLNodeStreamAgeSeconds.WithLabelValues(stream).
			Set(time.Since(info.ModTime()).Seconds())
	}
}
