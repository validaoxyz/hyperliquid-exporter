package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const tokioRuntimePollInterval = 60 * time.Second

// tokioStaleAfter is how old the newest sample may get before the per-task
// gauges are withdrawn. The feed appends about once a minute while alive,
// but has been observed to die for a day while the node kept running;
// re-emitting day-old cumulative values silently misleads dashboards.
const tokioStaleAfter = 15 * time.Minute

// tokioTaskAllowlist bounds label cardinality. On a mainnet non-validator
// peer the file contains 12 task names; on a validator we see 21 (the
// extras are the consensus + tx_forwarder + mempool refresher families).
// New tasks that appear in the future will land in this allowlist after
// a code review (with the same cardinality discipline as
// subsystem_latency_monitor).
var tokioTaskAllowlist = map[string]bool{
	// shared by both node roles
	"gossip rpc request handler":    true,
	"gossip rpc status":             true,
	"tokio_scheduled_observer_fast": true,
	"tokio_scheduled_observer_slow": true,
	"traffic_logger":                true,
	"lz4_stats":                     true,
	"gossip connection listener":    true,
	"validator connection listener": true,
	"node_disabler":                 true,
	"node_evm_request_handler":      true,
	// non-validator only
	"nv_stream_forward_client_blocks": true,
	"nv_stream_apply_execution_state": true,
	// validator-only (observed on a live testnet validator, May 2026)
	"client block or tx_forwarder":    true,
	"consensus out_recver":            true,
	"consensus rpc driver":            true,
	"consensus rpc request handler":   true,
	"consensus state":                 true,
	"external_tx_forwarder":           true,
	"external_tx_forwarder_forwarder": true,
	"external_tx_recver":              true,
	"mempool refresher":               true,
	"node_ip_updater":                 true,
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
	logger.InfoComponent("tokio_runtime", "watching %s", root)
	metrics.RegisterSource(metrics.SourceTokioRuntime, true)
	state := &tokioMonitorState{publishedTasks: make(map[string]struct{})}

	ticker := time.NewTicker(tokioRuntimePollInterval)
	defer ticker.Stop()

	if err := tickTokio(root, state, time.Now()); err != nil {
		logger.DebugComponent("tokio_runtime", "initial scan: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tickTokio(root, state, time.Now()); err != nil {
				logger.DebugComponent("tokio_runtime", "scan: %v", err)
			}
		}
	}
}

type tokioMonitorState struct {
	publishedTasks map[string]struct{}
}

func tickTokio(root string, state *tokioMonitorState, now time.Time) error {
	metrics.MarkMonitorAttempt("tokio_runtime")
	metrics.MarkSourceAttempt(metrics.SourceTokioRuntime)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			withdrawTokioSnapshot(state, true)
			metrics.MarkSourceAbsent(metrics.SourceTokioRuntime)
			metrics.MarkMonitorPublication("tokio_runtime")
			return nil
		}
		metrics.MarkSourceError(metrics.SourceTokioRuntime, metrics.SourceFailureStat)
		return fmt.Errorf("stat root: %w", err)
	}
	path, err := latestHourlyFile(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			withdrawTokioSnapshot(state, true)
			metrics.MarkSourceAvailable(metrics.SourceTokioRuntime)
			metrics.MarkSourcePublication(metrics.SourceTokioRuntime)
			metrics.MarkMonitorPublication("tokio_runtime")
			return nil
		}
		metrics.MarkSourceError(metrics.SourceTokioRuntime, metrics.SourceFailureWalk)
		return fmt.Errorf("latest hourly file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			withdrawTokioSnapshot(state, true)
			metrics.MarkSourceAbsent(metrics.SourceTokioRuntime)
			metrics.MarkMonitorPublication("tokio_runtime")
			return nil
		}
		metrics.MarkSourceError(metrics.SourceTokioRuntime, metrics.SourceFailureStat)
		return fmt.Errorf("stat latest: %w", err)
	}

	age := now.Sub(info.ModTime())
	if age < 0 {
		age = 0
	}
	if age > tokioStaleAfter {
		metrics.HLTokioSampleAgeSeconds.WithLabelValues().Set(age.Seconds())
		withdrawTokioSnapshot(state, false)
		metrics.MarkSourceReadOutcome(metrics.SourceTokioRuntime, true)
		metrics.MarkSourceSchemaOutcome(metrics.SourceTokioRuntime, true)
		metrics.MarkSourcePublication(metrics.SourceTokioRuntime)
		metrics.MarkMonitorPublication("tokio_runtime")
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceTokioRuntime, metrics.SourceFailureRead)
		return fmt.Errorf("read latest: %w", err)
	}
	snapshot, sampleTime, err := parseTokioSnapshot(data)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceTokioRuntime, metrics.SourceFailureSchema)
		return err
	}
	replaceTokioSnapshot(state, snapshot)
	if len(snapshot) == 0 {
		metrics.HLTokioSampleAgeSeconds.DeleteLabelValues()
		metrics.MarkSourceAvailable(metrics.SourceTokioRuntime)
	} else {
		metrics.HLTokioSampleAgeSeconds.WithLabelValues().Set(age.Seconds())
		metrics.MarkSourceValidObservation(metrics.SourceTokioRuntime, sampleTime)
		metrics.MarkMonitorValidObservation("tokio_runtime")
	}
	metrics.MarkSourcePublication(metrics.SourceTokioRuntime)
	metrics.MarkMonitorPublication("tokio_runtime")
	return nil
}

func deleteTokioSeries(task string) {
	metrics.HLTokioTaskPollSecondsTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskPollsTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskSlowPollsTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskLongDelaysTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskIdleSecondsTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskDroppedTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskScheduledTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskScheduledSecondsTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskFastPollsTotal.DeleteLabelValues(task)
	metrics.HLTokioTaskShortDelaysTotal.DeleteLabelValues(task)
}

func parseTokioLine(line []byte) (tokioTaskSample, bool) {
	s, _, err := parseTokioRecord(line)
	return s, err == nil
}

func parseTokioRecord(line []byte) (tokioTaskSample, time.Time, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return tokioTaskSample{}, time.Time{}, err
	}
	if len(outer) != 2 {
		return tokioTaskSample{}, time.Time{}, fmt.Errorf("invalid tokio tuple length")
	}
	var timestamp string
	if err := unmarshalRequiredJSON(outer[0], &timestamp); err != nil {
		return tokioTaskSample{}, time.Time{}, err
	}
	sampleTime, ok := parseVisorTime(timestamp)
	if !ok {
		return tokioTaskSample{}, time.Time{}, fmt.Errorf("invalid tokio timestamp")
	}
	var raw map[string]json.RawMessage
	if err := unmarshalRequiredJSON(outer[1], &raw); err != nil {
		return tokioTaskSample{}, time.Time{}, err
	}
	var s tokioTaskSample
	fields := []struct {
		name string
		dst  any
	}{
		{"task_name", &s.TaskName}, {"dropped_count", &s.DroppedCount},
		{"total_idle_duration", &s.TotalIdleDuration}, {"total_scheduled_count", &s.TotalScheduledCount},
		{"total_scheduled_duration", &s.TotalScheduledDuration}, {"total_poll_count", &s.TotalPollCount},
		{"total_poll_duration", &s.TotalPollDuration}, {"total_fast_poll_count", &s.TotalFastPollCount},
		{"total_slow_poll_count", &s.TotalSlowPollCount}, {"total_short_delay_count", &s.TotalShortDelayCount},
		{"total_long_delay_count", &s.TotalLongDelayCount},
	}
	for _, field := range fields {
		if err := unmarshalRequiredJSON(raw[field.name], field.dst); err != nil {
			return tokioTaskSample{}, time.Time{}, fmt.Errorf("required tokio field %s: %w", field.name, err)
		}
	}
	if s.TaskName == "" {
		return tokioTaskSample{}, time.Time{}, fmt.Errorf("empty task name")
	}
	if s.DroppedCount < 0 || s.TotalScheduledCount < 0 || s.TotalPollCount < 0 || s.TotalFastPollCount < 0 || s.TotalSlowPollCount < 0 || s.TotalShortDelayCount < 0 || s.TotalLongDelayCount < 0 ||
		math.IsNaN(s.TotalIdleDuration) || math.IsInf(s.TotalIdleDuration, 0) || s.TotalIdleDuration < 0 ||
		math.IsNaN(s.TotalScheduledDuration) || math.IsInf(s.TotalScheduledDuration, 0) || s.TotalScheduledDuration < 0 ||
		math.IsNaN(s.TotalPollDuration) || math.IsInf(s.TotalPollDuration, 0) || s.TotalPollDuration < 0 {
		return tokioTaskSample{}, time.Time{}, fmt.Errorf("invalid tokio counter or duration")
	}
	// Strip the per-validator "home_validator=Validator(0x…)" suffix from
	// the "listening for incoming connections" task. Without this strip,
	// every validator would have its own series label and dashboards
	// aggregating across nodes would split that task into N lines.
	if idx := strings.Index(s.TaskName, " home_validator="); idx > 0 {
		s.TaskName = s.TaskName[:idx]
	}
	return s, sampleTime, nil
}

func parseTokioSnapshot(data []byte) (map[string]tokioTaskSample, time.Time, error) {
	snapshot := make(map[string]tokioTaskSample)
	var newest time.Time
	lastComplete := bytes.LastIndexByte(data, '\n')
	if lastComplete < 0 {
		if len(bytes.TrimSpace(data)) > 0 {
			return nil, time.Time{}, fmt.Errorf("partial tokio record")
		}
		return snapshot, newest, nil
	}
	for _, raw := range bytes.Split(data[:lastComplete], []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		sample, sampleTime, err := parseTokioRecord(line)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("decode complete tokio record: %w", err)
		}
		if !tokioTaskAllowlist[sample.TaskName] {
			continue
		}
		snapshot[sample.TaskName] = sample
		if sampleTime.After(newest) {
			newest = sampleTime
		}
	}
	return snapshot, newest, nil
}

func replaceTokioSnapshot(state *tokioMonitorState, snapshot map[string]tokioTaskSample) {
	for task, sample := range snapshot {
		publishTokioSample(sample)
		state.publishedTasks[task] = struct{}{}
	}
	for task := range state.publishedTasks {
		if _, ok := snapshot[task]; !ok {
			deleteTokioSeries(task)
			delete(state.publishedTasks, task)
		}
	}
}

func withdrawTokioSnapshot(state *tokioMonitorState, deleteAge bool) {
	for task := range state.publishedTasks {
		deleteTokioSeries(task)
	}
	state.publishedTasks = make(map[string]struct{})
	if deleteAge {
		metrics.HLTokioSampleAgeSeconds.DeleteLabelValues()
	}
}

func publishTokioSample(s tokioTaskSample) {
	metrics.HLTokioTaskPollSecondsTotal.WithLabelValues(s.TaskName).Set(s.TotalPollDuration)
	metrics.HLTokioTaskPollsTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalPollCount))
	metrics.HLTokioTaskSlowPollsTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalSlowPollCount))
	metrics.HLTokioTaskLongDelaysTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalLongDelayCount))
	metrics.HLTokioTaskIdleSecondsTotal.WithLabelValues(s.TaskName).Set(s.TotalIdleDuration)
	metrics.HLTokioTaskDroppedTotal.WithLabelValues(s.TaskName).Set(float64(s.DroppedCount))
	metrics.HLTokioTaskScheduledTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalScheduledCount))
	metrics.HLTokioTaskScheduledSecondsTotal.WithLabelValues(s.TaskName).Set(s.TotalScheduledDuration)
	metrics.HLTokioTaskFastPollsTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalFastPollCount))
	metrics.HLTokioTaskShortDelaysTotal.WithLabelValues(s.TaskName).Set(float64(s.TotalShortDelayCount))
}
