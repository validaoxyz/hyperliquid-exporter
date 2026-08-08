package monitors

import (
	"context"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const monitorErrorDropLogInterval = time.Minute

var fixedErrorMonitorNames = map[string]struct{}{
	"accumulator_consensus": {},
	"block":                 {},
	"consensus":             {},
	"core_tx":               {},
	"crit_locations":        {},
	"crit_msg":              {},
	"disk":                  {},
	"evm":                   {},
	"evm_txs":               {},
	"gossip":                {},
	"gossip_connections":    {},
	"info_probe":            {},
	"log_lines":             {},
	"mempool":               {},
	"mempool_txs":           {},
	"node_state":            {},
	"operator_config":       {},
	"dominant_inbound":      {},
	"peer_set":              {},
	"process":               {},
	"proposal":              {},
	"public_ip":             {},
	"replay":                {},
	"replica":               {},
	"replica_runs":          {},
	"rocksdb":               {},
	"snapshot_status":       {},
	"subsystem_latency":     {},
	"subsystem_steps":       {},
	"tcp_connections":       {},
	"tcp_lz4":               {},
	"tcp_traffic":           {},
	"tmp_dir":               {},
	"tokio_runtime":         {},
	"update_checker":        {},
	"validator_api":         {},
	"validator_ip":          {},
	"validator_latency":     {},
	"validator_status":      {},
	"version":               {},
	"visor":                 {},
	"other":                 {},
}

var monitorErrorDropLogs = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

// ReportError attempts to deliver an error to one monitor's independent
// bounded channel without ever stalling the producer. A full channel drops
// only that report, increments bounded telemetry, and logs at most once per
// monitor per minute. Cancellation is not counted as a drop.
func ReportError(ctx context.Context, monitor string, errCh chan<- error, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case errCh <- err:
		return true
	default:
		monitor = fixedErrorMonitorName(monitor)
		metrics.IncMonitorErrorDrop(monitor)
		if shouldLogMonitorErrorDrop(monitor, time.Now()) {
			logger.WarningComponent(monitor, "monitor error report dropped because its error channel is full: %v", err)
		}
		return false
	}
}

func fixedErrorMonitorName(name string) string {
	if _, ok := fixedErrorMonitorNames[name]; ok {
		return name
	}
	return "other"
}

func shouldLogMonitorErrorDrop(monitor string, now time.Time) bool {
	monitorErrorDropLogs.Lock()
	defer monitorErrorDropLogs.Unlock()
	last := monitorErrorDropLogs.last[monitor]
	if !last.IsZero() && now.Sub(last) < monitorErrorDropLogInterval {
		return false
	}
	monitorErrorDropLogs.last[monitor] = now
	return true
}
