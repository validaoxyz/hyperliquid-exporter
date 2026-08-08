package monitors

import (
	"context"
	"encoding/json"
	"errors"
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

type jailingConfigSnapshot struct {
	absent                  bool
	dryRun                  *bool
	latencyThresholdSeconds *float64
	failureStage            metrics.SourceFailureStage
	err                     error
}

func tickOperatorConfig(root string) bool {
	metrics.MarkMonitorAttempt("operator_config")
	metrics.MarkSourceAttempt(metrics.SourceOperatorConfig)
	now := time.Now()
	if info, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			withdrawOperatorConfigSnapshot()
		} else {
			metrics.WithPrometheusSnapshotUpdate(func() {
				metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureStat)
			})
		}
		return false
	} else if !info.IsDir() {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureSchema)
		})
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
			metrics.WithPrometheusSnapshotUpdate(func() {
				metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureStat)
			})
			return false
		}
		if !info.Mode().IsRegular() {
			metrics.WithPrometheusSnapshotUpdate(func() {
				metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureSchema)
			})
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
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourceOperatorConfig, metrics.SourceFailureRead)
		})
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
	jailing := scanJailingConfig(root)
	if jailing.err != nil {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourceOperatorConfig, jailing.failureStage)
			metrics.IncMonitorError("operator_config")
		})
		logger.DebugComponent("operator_config", "read heartbeat_jailing_config.json: %v", jailing.err)
		return false
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
		metrics.HLNodeOperatorConfigFailedLoads.WithLabelValues().Set(float64(snapshot.failedAll))
		applyJailingConfigSnapshot(jailing)
		metrics.MarkSourceValidObservation(metrics.SourceOperatorConfig, time.Time{})
		metrics.MarkSourcePublication(metrics.SourceOperatorConfig)
		metrics.MarkMonitorValidObservation("operator_config")
		metrics.MarkMonitorPublication("operator_config")
	})
	return true
}

func withdrawOperatorConfigSnapshot() {
	metrics.WithPrometheusSnapshotUpdate(func() {
		for _, file := range operatorConfigFiles {
			metrics.HLNodeOperatorConfigPresent.DeleteLabelValues(file)
			metrics.HLNodeOperatorConfigAgeSeconds.DeleteLabelValues(file)
			metrics.HLNodeOperatorConfigFailedLoad.DeleteLabelValues(file)
		}
		metrics.HLNodeOperatorConfigFailedLoad.DeleteLabelValues("unknown")
		metrics.HLNodeOperatorConfigFailedLoads.DeleteLabelValues()
		withdrawJailingConfigSnapshot()
		metrics.MarkSourceAbsent(metrics.SourceOperatorConfig)
		metrics.MarkSourcePublication(metrics.SourceOperatorConfig)
		metrics.MarkMonitorPublication("operator_config")
	})
}

// scanJailingConfig stages the contents of heartbeat_jailing_config.json:
// the latency-EMA threshold this node uses when voting to jail peers, and
// whether enforcement is live or dry-run. The mtime age above only says the
// file changed; the threshold itself is the denominator of every
// jail-headroom question, e.g.
//
//	hl_consensus_validator_latency_ema_seconds / hl_node_jailing_threshold_seconds
func scanJailingConfig(root string) jailingConfigSnapshot {
	raw, err := os.ReadFile(filepath.Join(root, "heartbeat_jailing_config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return jailingConfigSnapshot{absent: true}
		}
		return jailingConfigSnapshot{failureStage: metrics.SourceFailureRead, err: err}
	}
	var jcfg struct {
		DryRun                  *bool    `json:"dry_run"`
		LatencyEmaJailThreshold *float64 `json:"latency_ema_jail_threshold"`
	}
	if err := json.Unmarshal(raw, &jcfg); err != nil {
		return jailingConfigSnapshot{failureStage: metrics.SourceFailureDecode, err: err}
	}
	if jcfg.DryRun == nil || jcfg.LatencyEmaJailThreshold == nil {
		return jailingConfigSnapshot{
			failureStage: metrics.SourceFailureSchema,
			err:          errors.New("jailing config requires dry_run and latency_ema_jail_threshold"),
		}
	}
	return jailingConfigSnapshot{
		dryRun:                  jcfg.DryRun,
		latencyThresholdSeconds: jcfg.LatencyEmaJailThreshold,
	}
}

func applyJailingConfigSnapshot(snapshot jailingConfigSnapshot) {
	if snapshot.err != nil {
		return
	}
	if snapshot.absent {
		withdrawJailingConfigSnapshot()
		return
	}
	if snapshot.latencyThresholdSeconds == nil || snapshot.dryRun == nil {
		return
	}

	metrics.InitJailingConfigInstruments()
	metrics.HLNodeJailingThresholdSeconds.WithLabelValues().Set(*snapshot.latencyThresholdSeconds)
	v := 0.0
	if *snapshot.dryRun {
		v = 1
	}
	metrics.HLNodeJailingDryRun.WithLabelValues().Set(v)
}

func withdrawJailingConfigSnapshot() {
	if metrics.HLNodeJailingThresholdSeconds != nil {
		metrics.HLNodeJailingThresholdSeconds.DeleteLabelValues()
	}
	if metrics.HLNodeJailingDryRun != nil {
		metrics.HLNodeJailingDryRun.DeleteLabelValues()
	}
}
