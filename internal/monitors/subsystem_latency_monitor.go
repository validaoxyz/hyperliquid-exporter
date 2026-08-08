package monitors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// subsystemAllowlist is the set of hl-node internal subsystems we expose
// as Prometheus series. Capped explicitly to avoid label-cardinality
// surprises if new subsystems appear under latency_summaries/. Operators
// should care primarily about block-production-path latencies and the
// async runtime observers.
//
// Subsystems found on a live mainnet peer (May 2026):
//
//	bucket_guard, execution_sender, handle_request_rate_limiter,
//	node_fast_backlog_from_node, node_fast_begin_block_to_commit,
//	node_fast_block_duration, node_slow_backlog_from_node,
//	node_slow_begin_block_to_commit, node_slow_block_duration, proposer,
//	run_node_compute_resps_hash, tcp_lz4, tokio_scheduled_observer_fast,
//	tokio_scheduled_observer_slow, tokio_spawn_forever_scheduled
//
// All of these are useful for an operator dashboard; the allowlist is
// here as an explicit ceiling rather than to filter known noise.
var subsystemAllowlist = map[string]bool{
	"bucket_guard":                    true,
	"execution_sender":                true,
	"handle_request_rate_limiter":     true,
	"node_fast_backlog_from_node":     true,
	"node_fast_begin_block_to_commit": true,
	"node_fast_block_duration":        true,
	"node_slow_backlog_from_node":     true,
	"node_slow_begin_block_to_commit": true,
	"node_slow_block_duration":        true,
	"proposer":                        true,
	"run_node_compute_resps_hash":     true,
	"tcp_lz4":                         true,
	"tokio_scheduled_observer_fast":   true,
	"tokio_scheduled_observer_slow":   true,
	"tokio_spawn_forever_scheduled":   true,
}

// Note: validator-only subsystems `consensus` and `l1_task_latency` are
// nested (sub-step) subsystems — their leaves live at
// <subsystem>/<step>/<YYYYMMDD>, not at <subsystem>/<YYYYMMDD>. They're
// exposed via [[subsystem_steps_monitor]] instead. The other observed
// validator-only subsystem dirs (gossip_broadcast_send_duration,
// node_begin_block_to_node_replica_wall_clock, node_replica_*,
// node_slow_abci_*, validator_broadcast_send_duration) are present on
// disk but empty under normal operation on the test validator — no
// leaf files to read. If they start being populated, decide flat vs
// nested per-subsystem and add to the appropriate allowlist.

// latencySummary mirrors one JSON line under latency_summaries/<sub>/<date>.
// Fields are floats representing seconds.
type latencySummary struct {
	Time           string  `json:"time"`
	TotalN         int64   `json:"total_n"`
	TotalMean      float64 `json:"total_mean"`
	Mean           float64 `json:"mean"`
	Median         float64 `json:"med"`
	P90            float64 `json:"p90"`
	P95            float64 `json:"p95"`
	Max            float64 `json:"max"`
	StdDev         float64 `json:"std_dev"`
	WorkFrac       float64 `json:"work_frac"`
	BucketWorkFrac float64 `json:"bucket_work_frac"`
}

const (
	subsystemLatencyPollInterval = 30 * time.Second
	subsystemLatencyStaleAfter   = 15 * time.Minute
)

var (
	errNoCompleteSummary = errors.New("no complete latency summary")
	errPartialSummary    = errors.New("partial latency summary")
)

type subsystemLatencyState struct {
	published map[string]struct{}
}

// StartSubsystemLatencyMonitor scans $NODE_HOME/data/latency_summaries every
// subsystemLatencyPollInterval. For each allowlisted subsystem it reads the
// last record of the newest date-file and publishes that to the
// subsystem-latency gauges.
//
// Reading the last record means restart-tolerance is implicit (each tick
// re-fetches; no resume state to lose).
func StartSubsystemLatencyMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "latency_summaries")
	logger.InfoComponent("subsystem_latency", "watching %s", root)
	metrics.RegisterSource(metrics.SourceSubsystemLatency, true)
	state := &subsystemLatencyState{published: make(map[string]struct{})}

	ticker := time.NewTicker(subsystemLatencyPollInterval)
	defer ticker.Stop()

	if err := tickSubsystemLatency(root, state, time.Now()); err != nil {
		logger.DebugComponent("subsystem_latency", "initial scan: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tickSubsystemLatency(root, state, time.Now()); err != nil {
				logger.DebugComponent("subsystem_latency", "scan: %v", err)
			}
		}
	}
}

func tickSubsystemLatency(root string, state *subsystemLatencyState, now time.Time) error {
	metrics.MarkMonitorAttempt("subsystem_latency")
	metrics.MarkSourceAttempt(metrics.SourceSubsystemLatency)
	subDirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			withdrawSubsystemLatency(state)
			metrics.MarkSourceAbsent(metrics.SourceSubsystemLatency)
			metrics.MarkMonitorPublication("subsystem_latency")
			return nil
		}
		metrics.MarkSourceError(metrics.SourceSubsystemLatency, metrics.SourceFailureRead)
		return fmt.Errorf("read root: %w", err)
	}

	snapshot := make(map[string]latencySummary)
	var newest time.Time
	for _, sub := range subDirs {
		if !sub.IsDir() {
			continue
		}
		name := sub.Name()
		if !subsystemAllowlist[name] {
			continue
		}
		path := filepath.Join(root, name)
		datePath, err := latestDateFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			metrics.MarkSourceError(metrics.SourceSubsystemLatency, metrics.SourceFailureRead)
			return fmt.Errorf("latest date for %s: %w", name, err)
		}
		summary, sampleTime, err := readLastSummaryComplete(datePath)
		if err != nil {
			if errors.Is(err, errNoCompleteSummary) {
				continue
			}
			metrics.MarkSourceError(metrics.SourceSubsystemLatency, metrics.SourceFailureSchema)
			return fmt.Errorf("summary %s: %w", name, err)
		}
		if now.Sub(sampleTime) > subsystemLatencyStaleAfter {
			continue
		}
		snapshot[name] = summary
		if sampleTime.After(newest) {
			newest = sampleTime
		}
	}

	replaceSubsystemLatency(state, snapshot)
	if len(snapshot) == 0 {
		metrics.MarkSourceAvailable(metrics.SourceSubsystemLatency)
	} else {
		metrics.MarkSourceValidObservation(metrics.SourceSubsystemLatency, newest)
		metrics.MarkMonitorValidObservation("subsystem_latency")
	}
	metrics.MarkSourcePublication(metrics.SourceSubsystemLatency)
	metrics.MarkMonitorPublication("subsystem_latency")
	return nil
}

// latestDateFile returns the highest-named (lexicographically — which is
// chronological for YYYYMMDD-prefixed files) entry in dir.
func latestDateFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return "", os.ErrNotExist
	}
	return filepath.Join(dir, best), nil
}

// readLastSummary returns the last complete JSON record in the file. Reads
// the tail bytes only (4 KiB is plenty — each record is ~400 bytes) so it
// is fast for the 1 MB-plus daily files.
func readLastSummary(path string) (latencySummary, bool) {
	s, _, err := readLastSummaryComplete(path)
	return s, err == nil
}

// readLastSummaryComplete returns only a newline-terminated JSONL record.
// An unterminated tail is retained by the writer and ignored here; a malformed
// complete record rejects the scan so callers keep the prior generation.
func readLastSummaryComplete(path string) (latencySummary, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return latencySummary{}, time.Time{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return latencySummary{}, time.Time{}, err
	}
	const window = 4096
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return latencySummary{}, time.Time{}, err
	}
	buf, err := io.ReadAll(bufio.NewReader(f))
	if err != nil {
		return latencySummary{}, time.Time{}, err
	}
	if start > 0 {
		if firstNL := strings.IndexByte(string(buf), '\n'); firstNL >= 0 {
			buf = buf[firstNL+1:]
		} else {
			// A non-empty bounded tail without a delimiter is an oversized
			// unterminated record, not a successfully empty source. Reject the
			// scan so callers retain the previous complete snapshot.
			return latencySummary{}, time.Time{}, errPartialSummary
		}
	}
	lastNL := strings.LastIndexByte(string(buf), '\n')
	if lastNL < 0 {
		if len(bytes.TrimSpace(buf)) == 0 {
			return latencySummary{}, time.Time{}, errNoCompleteSummary
		}
		return latencySummary{}, time.Time{}, errPartialSummary
	}
	lines := strings.Split(strings.TrimSpace(string(buf[:lastNL])), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) == "" {
		return latencySummary{}, time.Time{}, errNoCompleteSummary
	}
	line := strings.TrimSpace(lines[len(lines)-1])
	s, err := parseLatencySummaryRecord([]byte(line))
	if err != nil {
		return latencySummary{}, time.Time{}, fmt.Errorf("decode complete record: %w", err)
	}
	sampleTime, ok := parseVisorTime(s.Time)
	if !ok {
		return latencySummary{}, time.Time{}, fmt.Errorf("invalid sample time")
	}
	return s, sampleTime, nil
}

func parseLatencySummaryRecord(line []byte) (latencySummary, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return latencySummary{}, err
	}
	var summary latencySummary
	fields := []struct {
		name string
		dst  any
	}{
		{"time", &summary.Time}, {"total_n", &summary.TotalN}, {"total_mean", &summary.TotalMean},
		{"mean", &summary.Mean}, {"med", &summary.Median}, {"p90", &summary.P90},
		{"p95", &summary.P95}, {"max", &summary.Max}, {"std_dev", &summary.StdDev},
		{"work_frac", &summary.WorkFrac},
	}
	for _, field := range fields {
		if err := unmarshalRequiredJSON(raw[field.name], field.dst); err != nil {
			return latencySummary{}, fmt.Errorf("required field %s: %w", field.name, err)
		}
	}
	if summary.TotalN < 0 {
		return latencySummary{}, fmt.Errorf("negative total_n")
	}
	for _, value := range []float64{summary.TotalMean, summary.Mean, summary.Median, summary.P90, summary.P95, summary.Max, summary.StdDev, summary.WorkFrac} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return latencySummary{}, fmt.Errorf("non-finite summary value")
		}
	}
	return summary, nil
}

func replaceSubsystemLatency(state *subsystemLatencyState, snapshot map[string]latencySummary) {
	for subsystem, summary := range snapshot {
		publishSubsystemLatency(subsystem, summary)
	}
	for subsystem := range state.published {
		if _, ok := snapshot[subsystem]; !ok {
			deleteSubsystemLatency(subsystem)
		}
	}
	state.published = make(map[string]struct{}, len(snapshot))
	for subsystem := range snapshot {
		state.published[subsystem] = struct{}{}
	}
}

func withdrawSubsystemLatency(state *subsystemLatencyState) {
	for subsystem := range state.published {
		deleteSubsystemLatency(subsystem)
	}
	state.published = make(map[string]struct{})
}

func deleteSubsystemLatency(subsystem string) {
	metrics.HLNodeSubsystemLatencyMean.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemLatencyMedian.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemLatencyP90.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemLatencyP95.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemLatencyMax.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemLatencyStdDev.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemWorkFrac.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemSamplesTotal.DeleteLabelValues(subsystem)
	metrics.HLNodeSubsystemLatencyLifetimeMean.DeleteLabelValues(subsystem)
}

func publishSubsystemLatency(subsystem string, s latencySummary) {
	metrics.HLNodeSubsystemLatencyMean.WithLabelValues(subsystem).Set(s.Mean)
	metrics.HLNodeSubsystemLatencyMedian.WithLabelValues(subsystem).Set(s.Median)
	metrics.HLNodeSubsystemLatencyP90.WithLabelValues(subsystem).Set(s.P90)
	metrics.HLNodeSubsystemLatencyP95.WithLabelValues(subsystem).Set(s.P95)
	metrics.HLNodeSubsystemLatencyMax.WithLabelValues(subsystem).Set(s.Max)
	metrics.HLNodeSubsystemLatencyStdDev.WithLabelValues(subsystem).Set(s.StdDev)
	metrics.HLNodeSubsystemWorkFrac.WithLabelValues(subsystem).Set(s.WorkFrac)
	metrics.HLNodeSubsystemSamplesTotal.WithLabelValues(subsystem).Set(float64(s.TotalN))
	metrics.HLNodeSubsystemLatencyLifetimeMean.WithLabelValues(subsystem).Set(s.TotalMean)
}
