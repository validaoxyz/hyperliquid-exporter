package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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
//   bucket_guard, execution_sender, handle_request_rate_limiter,
//   node_fast_backlog_from_node, node_fast_begin_block_to_commit,
//   node_fast_block_duration, node_slow_backlog_from_node,
//   node_slow_begin_block_to_commit, node_slow_block_duration, proposer,
//   run_node_compute_resps_hash, tcp_lz4, tokio_scheduled_observer_fast,
//   tokio_scheduled_observer_slow, tokio_spawn_forever_scheduled
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
	Time          string  `json:"time"`
	TotalN        int64   `json:"total_n"`
	TotalMean     float64 `json:"total_mean"`
	Mean          float64 `json:"mean"`
	Median        float64 `json:"med"`
	P90           float64 `json:"p90"`
	P95           float64 `json:"p95"`
	Max           float64 `json:"max"`
	StdDev        float64 `json:"std_dev"`
	WorkFrac      float64 `json:"work_frac"`
	BucketWorkFrac float64 `json:"bucket_work_frac"`
}

const subsystemLatencyPollInterval = 30 * time.Second

// StartSubsystemLatencyMonitor scans $NODE_HOME/data/latency_summaries every
// subsystemLatencyPollInterval. For each allowlisted subsystem it reads the
// last record of the newest date-file and publishes that to the
// subsystem-latency gauges.
//
// Reading the last record means restart-tolerance is implicit (each tick
// re-fetches; no resume state to lose).
func StartSubsystemLatencyMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "latency_summaries")

	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("subsystem_latency",
			"latency_summaries directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("subsystem_latency", "watching %s", root)

	ticker := time.NewTicker(subsystemLatencyPollInterval)
	defer ticker.Stop()

	tickSubsystemLatency(root)
	metrics.MarkMonitorTick("subsystem_latency")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickSubsystemLatency(root)
			metrics.MarkMonitorTick("subsystem_latency")
		}
	}
}

func tickSubsystemLatency(root string) {
	subDirs, err := os.ReadDir(root)
	if err != nil {
		logger.DebugComponent("subsystem_latency", "read root: %v", err)
		return
	}
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
			continue
		}
		summary, ok := readLastSummary(datePath)
		if !ok {
			continue
		}
		publishSubsystemLatency(name, summary)
	}
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
	f, err := os.Open(path)
	if err != nil {
		return latencySummary{}, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return latencySummary{}, false
	}
	const window = 4096
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return latencySummary{}, false
	}
	buf, err := io.ReadAll(bufio.NewReader(f))
	if err != nil {
		return latencySummary{}, false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	// scan upwards for the last parseable line — guards against a torn
	// trailing write from the node.
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var s latencySummary
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			return s, true
		}
	}
	return latencySummary{}, false
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
}
