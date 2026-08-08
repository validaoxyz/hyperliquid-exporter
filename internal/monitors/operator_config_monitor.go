package monitors

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	// gossip-auction ordering toggle (2026-04 hl-node addition)
	"node_gossip_priority_config.json",
	// Presence only. The body is versioned/undocumented and is deliberately
	// not interpreted by the exporter.
	"bug_alert_ack.json",
}

// StartOperatorConfigMonitor publishes the mtime age of each file under
// $NODE_HOME/file_mod_time_tracker/. The age is a proxy for "when did
// the operator last touch this config" — useful in postmortems.
func StartOperatorConfigMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceOperatorConfig, true)
	root := filepath.Join(cfg.NodeHome, "file_mod_time_tracker")
	logger.InfoComponent("operator_config", "watching %s with late-source discovery", root)

	ticker := time.NewTicker(operatorConfigPollInterval)
	defer ticker.Stop()

	tickOperatorConfig(root)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickOperatorConfig(root)
		}
	}
}

type operatorConfigSnapshot struct {
	present    map[string]float64
	ageSeconds map[string]*float64
	failed     map[string]int64
	failedAll  int64
}

func tickOperatorConfig(root string) bool {
	metrics.MarkMonitorAttempt("operator_config")
	metrics.MarkSourceAttempt(metrics.SourceOperatorConfig)
	now := time.Now()
	if info, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			metrics.MarkSourceAbsent(metrics.SourceOperatorConfig)
		} else {
			metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureStat)
		}
		return false
	} else if !info.IsDir() {
		metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureSchema)
		return false
	}

	snapshot := operatorConfigSnapshot{
		present:    make(map[string]float64, len(operatorConfigFiles)),
		ageSeconds: make(map[string]*float64, len(operatorConfigFiles)),
		failed:     make(map[string]int64, len(operatorConfigFiles)+1),
	}
	for _, file := range operatorConfigFiles {
		info, err := os.Stat(filepath.Join(root, file))
		if err != nil {
			if os.IsNotExist(err) {
				snapshot.present[file] = 0
				continue
			}
			metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureStat)
			return false
		}
		if !info.Mode().IsRegular() {
			metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureSchema)
			return false
		}
		snapshot.present[file] = 1
		age := now.Sub(info.ModTime()).Seconds()
		if age < 0 {
			age = 0
		}
		ageCopy := age
		snapshot.ageSeconds[file] = &ageCopy
	}

	// Count *_FAILED_LOAD sidecars hl-node leaves when it rejects an
	// operator-pushed config. Any non-zero count = silent
	// misconfiguration; the operator pushed firewall_ips.json or
	// similar and it never actually loaded.
	entries, err := os.ReadDir(root)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureRead)
		return false
	}
	known := make(map[string]struct{}, len(operatorConfigFiles))
	for _, file := range operatorConfigFiles {
		known[file] = struct{}{}
		snapshot.failed[file] = 0
	}
	snapshot.failed["unknown"] = 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_FAILED_LOAD") {
			continue
		}
		snapshot.failedAll++
		file := strings.TrimSuffix(entry.Name(), "_FAILED_LOAD")
		if _, ok := known[file]; !ok {
			file = "unknown"
		}
		snapshot.failed[file]++
	}

	metrics.WithPrometheusSnapshotUpdate(func() {
		for _, file := range operatorConfigFiles {
			metrics.HLNodeOperatorConfigPresent.WithLabelValues(file).Set(snapshot.present[file])
			if age := snapshot.ageSeconds[file]; age == nil {
				metrics.HLNodeOperatorConfigAgeSeconds.DeleteLabelValues(file)
			} else {
				metrics.HLNodeOperatorConfigAgeSeconds.WithLabelValues(file).Set(*age)
			}
			metrics.HLNodeOperatorConfigFailedLoad.WithLabelValues(file).Set(float64(snapshot.failed[file]))
		}
		metrics.HLNodeOperatorConfigFailedLoad.WithLabelValues("unknown").Set(float64(snapshot.failed["unknown"]))
		metrics.HLNodeOperatorConfigFailedLoads.Set(float64(snapshot.failedAll))
		metrics.MarkSourceValidObservation(metrics.SourceOperatorConfig, time.Time{})
		metrics.MarkSourcePublication(metrics.SourceOperatorConfig)
		metrics.MarkMonitorValidObservation("operator_config")
		metrics.MarkMonitorPublication("operator_config")
	})

	readJailingConfig(root)
	return true
}

// readJailingConfig publishes the contents of heartbeat_jailing_config.json:
// the latency-EMA threshold this node uses when voting to jail peers, and
// whether enforcement is live or dry-run. The mtime age above only says the
// file changed; the threshold itself is the denominator of every
// jail-headroom question, e.g.
//
//	hl_consensus_validator_latency_ema_seconds / hl_node_jailing_threshold_seconds
func readJailingConfig(root string) {
	raw, err := os.ReadFile(filepath.Join(root, "heartbeat_jailing_config.json"))
	if err != nil {
		return // non-validator nodes don't have the file
	}
	var jcfg struct {
		DryRun                  *bool    `json:"dry_run"`
		LatencyEmaJailThreshold *float64 `json:"latency_ema_jail_threshold"`
	}
	if err := json.Unmarshal(raw, &jcfg); err != nil {
		logger.DebugComponent("operator_config", "parse heartbeat_jailing_config.json: %v", err)
		return
	}
	if jcfg.LatencyEmaJailThreshold == nil && jcfg.DryRun == nil {
		return
	}

	metrics.InitJailingConfigInstruments()
	if jcfg.LatencyEmaJailThreshold != nil {
		metrics.HLNodeJailingThresholdSeconds.Set(*jcfg.LatencyEmaJailThreshold)
	}
	if jcfg.DryRun != nil {
		v := 0.0
		if *jcfg.DryRun {
			v = 1
		}
		metrics.HLNodeJailingDryRun.Set(v)
	}
}
