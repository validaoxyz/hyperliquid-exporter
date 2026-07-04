package monitors

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const rateLimitedPollInterval = 60 * time.Second

// rateLimitedStreams is the fixed set of rate-limiter streams hl-node
// writes under data/rate_limited_ips/.
var rateLimitedStreams = []string{
	"abci_stream",
	"gossip_rpc_blocks",
	"gossip_rpc_requests",
}

// StartRateLimitedMonitor counts non-empty files in the newest date dir of
// each data/rate_limited_ips stream. hl-node only writes these when it
// actively rate-limits a peer, so a healthy node sits at zero and any
// non-zero value is a cheap abuse/DoS tripwire. The record schema is
// deliberately not parsed (never observed populated on our nodes); the
// file count alone carries the signal.
func StartRateLimitedMonitor(ctx context.Context, cfg config.Config) {
	root := filepath.Join(cfg.NodeHome, "data", "rate_limited_ips")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("rate_limited",
			"rate_limited_ips directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("rate_limited", "watching %s", root)

	ticker := time.NewTicker(rateLimitedPollInterval)
	defer ticker.Stop()

	tickRateLimited(root)
	metrics.MarkMonitorTick("rate_limited")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickRateLimited(root)
			metrics.MarkMonitorTick("rate_limited")
		}
	}
}

func tickRateLimited(root string) {
	for _, stream := range rateLimitedStreams {
		metrics.HLNodeRateLimitedFiles.WithLabelValues(stream).
			Set(float64(countNonEmptyNewestDate(filepath.Join(root, stream, "hourly"))))
	}
}

// countNonEmptyNewestDate returns how many non-empty regular files sit in
// the lexicographically-newest date dir under hourlyRoot; 0 when the
// layout is absent or empty.
func countNonEmptyNewestDate(hourlyRoot string) int {
	entries, err := os.ReadDir(hourlyRoot)
	if err != nil {
		return 0
	}
	dates := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dates = append(dates, e.Name())
		}
	}
	if len(dates) == 0 {
		return 0
	}
	sort.Strings(dates)
	newest := filepath.Join(hourlyRoot, dates[len(dates)-1])

	files, err := os.ReadDir(newest)
	if err != nil {
		return 0
	}
	count := 0
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if info, err := f.Info(); err == nil && info.Size() > 0 {
			count++
		}
	}
	return count
}
