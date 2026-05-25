package monitors

import (
	"bytes"
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

const tokioRuntimePollInterval = 60 * time.Second

// tokioTaskAllowlist bounds label cardinality. On a mainnet non-validator
// peer the file contains 12 task names; on a validator we see 21 (the
// extras are the consensus + tx_forwarder + mempool refresher families).
// New tasks that appear in the future will land in this allowlist after
// a code review (with the same cardinality discipline as
// subsystem_latency_monitor).
var tokioTaskAllowlist = map[string]bool{
	// shared by both node roles
	"gossip rpc request handler":      true,
	"gossip rpc status":               true,
	"tokio_scheduled_observer_fast":   true,
	"tokio_scheduled_observer_slow":   true,
	"traffic_logger":                  true,
	"lz4_stats":                       true,
	"gossip connection listener":      true,
	"validator connection listener":   true,
	"node_disabler":                   true,
	"node_evm_request_handler":        true,
	// non-validator only
	"nv_stream_forward_client_blocks": true,
	"nv_stream_apply_execution_state": true,
	// validator-only (observed on a live testnet validator, May 2026)
	"client block or tx_forwarder":      true,
	"consensus out_recver":              true,
	"consensus rpc driver":              true,
	"consensus rpc request handler":     true,
	"consensus state":                   true,
	"external_tx_forwarder":             true,
	"external_tx_forwarder_forwarder":   true,
	"external_tx_recver":                true,
	"mempool refresher":                 true,
	"node_ip_updater":                   true,
	// rewritten label for the per-validator "listening for incoming
	// connections home_validator=Validator(0x…)" task. We strip the
	// embedded address in parseTokioLine so label cardinality stays at
	// 1 per node instead of N (and dashboards aggregate cleanly across
	// validators).
	"listening for incoming connections": true,
}

// tokioTaskSample mirrors one record of the JSON-lines file.
type tokioTaskSample struct {
	TaskName               string  `json:"task_name"`
	InstrumentedCount      int64   `json:"instrumented_count"`
	DroppedCount           int64   `json:"dropped_count"`
	TotalIdleDuration      float64 `json:"total_idle_duration"`
	TotalScheduledCount    int64   `json:"total_scheduled_count"`
	TotalScheduledDuration float64 `json:"total_scheduled_duration"`
	TotalPollCount         int64   `json:"total_poll_count"`
	TotalPollDuration      float64 `json:"total_poll_duration"`
	TotalFastPollCount     int64   `json:"total_fast_poll_count"`
	TotalSlowPollCount     int64   `json:"total_slow_poll_count"`
	TotalShortDelayCount   int64   `json:"total_short_delay_count"`
	TotalLongDelayCount    int64   `json:"total_long_delay_count"`
}

// StartTokioRuntimeMonitor watches
// $NODE_HOME/data/tokio_spawn_forever_metrics/hourly/<date>/<hour>.
//
// On each tick: read the tail of the latest hour-file, decode the last
// JSON-line record per allowlisted task name, publish the cumulative
// counters. Modeled as gauges (not Prometheus counters) because the
// node's "total_*" values reset on process restart, and a counter
// regression would confuse rate() queries.
//
// Operator signal: total_slow_poll_count and total_long_delay_count
// climbing fast = the async runtime is starved or a task is doing
// CPU-heavy work in poll(). dropped_count > 0 = task panicked.
func StartTokioRuntimeMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "tokio_spawn_forever_metrics", "hourly")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("tokio_runtime",
			"tokio_spawn_forever_metrics directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("tokio_runtime", "watching %s", root)

	ticker := time.NewTicker(tokioRuntimePollInterval)
	defer ticker.Stop()

	tickTokio(root)
	metrics.MarkMonitorTick("tokio_runtime")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickTokio(root)
			metrics.MarkMonitorTick("tokio_runtime")
		}
	}
}

func tickTokio(root string) {
	path, err := latestHourlyFile(root)
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	// Walk lines backwards, keeping the FIRST sample we see for each
	// allowlisted task name. Stop once we have every allowed name OR
	// run out of file.
	seen := map[string]bool{}
	want := len(tokioTaskAllowlist)
	end := len(data)
	for end > 0 && len(seen) < want {
		for end > 0 && data[end-1] == '\n' {
			end--
		}
		if end == 0 {
			break
		}
		start := bytes.LastIndexByte(data[:end], '\n') + 1
		line := bytes.TrimSpace(data[start:end])
		end = start - 1
		if len(line) == 0 || line[0] != '[' {
			continue
		}
		s, ok := parseTokioLine(line)
		if !ok || !tokioTaskAllowlist[s.TaskName] || seen[s.TaskName] {
			continue
		}
		publishTokioSample(s)
		seen[s.TaskName] = true
	}
}

func parseTokioLine(line []byte) (tokioTaskSample, bool) {
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return tokioTaskSample{}, false
	}
	var s tokioTaskSample
	if err := json.Unmarshal(outer[1], &s); err != nil {
		return tokioTaskSample{}, false
	}
	if s.TaskName == "" {
		return tokioTaskSample{}, false
	}
	// Strip the per-validator "home_validator=Validator(0x…)" suffix from
	// the "listening for incoming connections" task. Without this strip,
	// every validator would have its own series label and dashboards
	// aggregating across nodes would split that task into N lines.
	if idx := strings.Index(s.TaskName, " home_validator="); idx > 0 {
		s.TaskName = s.TaskName[:idx]
	}
	return s, true
}

func publishTokioSample(s tokioTaskSample) {
	metrics.HLTokioTaskPollSecondsTotal.WithLabelValues(s.TaskName).Set(s.TotalPollDuration)
	metrics.HLTokioTaskPollsTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalPollCount))
	metrics.HLTokioTaskSlowPollsTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalSlowPollCount))
	metrics.HLTokioTaskLongDelaysTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalLongDelayCount))
	metrics.HLTokioTaskIdleSecondsTotal.WithLabelValues(s.TaskName).Set(s.TotalIdleDuration)
	metrics.HLTokioTaskDroppedTotal.WithLabelValues(s.TaskName).Set(float64(s.DroppedCount))
}
