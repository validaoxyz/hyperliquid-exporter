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

const operatorConfigPollInterval = 5 * time.Minute

// operatorConfigFiles is the fixed set of operator-edited JSONs under
// file_mod_time_tracker/. Cardinality is bounded by this list (which we
// review when hl-node adds new entries).
var operatorConfigFiles = []string{
	"crit_msg_ignore.json",
	"firewall_ips.json",
	"node_firewall_ips.json",
	"ip_rate_limiter_alert_config.json",
	"n_gossip_peers.json",
	// validator-only: controls whether the node auto-jails peers by
	// latency EMA. dry_run=true means it logs but doesn't act.
	"heartbeat_jailing_config.json",
}

// StartOperatorConfigMonitor publishes the mtime age of each file under
// $NODE_HOME/file_mod_time_tracker/. The age is a proxy for "when did
// the operator last touch this config" — useful in postmortems.
func StartOperatorConfigMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "file_mod_time_tracker")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("operator_config",
			"file_mod_time_tracker directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("operator_config", "watching %s", root)

	ticker := time.NewTicker(operatorConfigPollInterval)
	defer ticker.Stop()

	tickOperatorConfig(root)
	metrics.MarkMonitorTick("operator_config")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickOperatorConfig(root)
			metrics.MarkMonitorTick("operator_config")
		}
	}
}

func tickOperatorConfig(root string) {
	now := time.Now()
	for _, f := range operatorConfigFiles {
		info, err := os.Stat(filepath.Join(root, f))
		if err != nil {
			continue
		}
		metrics.HLNodeOperatorConfigAgeSeconds.WithLabelValues(f).
			Set(now.Sub(info.ModTime()).Seconds())
	}

	// Count *_FAILED_LOAD sidecars hl-node leaves when it rejects an
	// operator-pushed config. Any non-zero count = silent
	// misconfiguration; the operator pushed firewall_ips.json or
	// similar and it never actually loaded.
	var failed int64
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if len(e.Name()) > 12 && e.Name()[len(e.Name())-12:] == "_FAILED_LOAD" {
				failed++
			}
		}
	}
	metrics.HLNodeOperatorConfigFailedLoads.Set(float64(failed))
}
