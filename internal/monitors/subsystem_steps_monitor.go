package monitors

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// bucketGuardSteps is the allowlist of bucket_guard sub-steps we expose
// individually. Mirrors what's on disk under
// latency_summaries/bucket_guard/<step>/<YYYYMMDD>. Sourced from a
// live mainnet peer in May 2026.
var bucketGuardSteps = map[string]bool{
	"action_delayer_log_status":                  true,
	"apply_bole_liquidations":                    true,
	"begin_block":                                true,
	"build_big_evm_block_and_apply_l1_effects":   true,
	"build_small_evm_block_and_apply_l1_effects": true,
	"c_staking_stage_reward":                     true,
	"cancel_aggressive_orders_at_oi_cap":         true,
	"decay_staking":                              true,
	"deterministic_vty_alert_n_bucket_samples":   true,
	"distribute_funding":                         true,
	"gas_auction_restart":                        true,
	"hyperliquidity_ensure_orders":               true,
	"prune_book_empty_user_states":               true,
	"refresh_hip3_stale_mark_pxs":                true,
	"reset_counters":                             true,
	"reset_recent_ois":                           true,
	"slow_abci_engine_read_increment":            true,
	"update_funding_rates":                       true,
	"validator_l1_vote_tracker_prune_expired":    true,
}

// tcpLz4Steps is the allowlist of per-direction-per-port lz4 latency
// sub-steps. Cardinality 4.
var tcpLz4Steps = map[string]bool{
	"in_4001":  true,
	"out_4001": true,
	"in_4002":  true,
	"out_4002": true,
}

const subsystemStepsPollInterval = 60 * time.Second

// StartSubsystemStepsMonitor surfaces the finer-grained latency
// breakdowns nested under latency_summaries/bucket_guard/<step>/<date>
// and latency_summaries/tcp_lz4/<step>/<date>. These steps don't
// appear at the subsystem level — bucket_guard has 19 distinct work
// items and tcp_lz4 has separate latency profiles per direction-port
// pair.
//
// Operator signal: `bucket_guard.begin_block` p95 may look healthy
// while max occasionally spikes into seconds — a tail-latency outlier
// invisible from the rolled-up subsystem stats. Same for the
// per-port lz4 latencies, which show the 4002 port runs ~10x
// slower than 4001 (an undocumented quirk worth knowing).
func StartSubsystemStepsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	bucketGuardRoot := filepath.Join(cfg.NodeHome, "data", "latency_summaries", "bucket_guard")
	tcpLz4Root := filepath.Join(cfg.NodeHome, "data", "latency_summaries", "tcp_lz4")

	if _, err := os.Stat(bucketGuardRoot); err != nil {
		logger.InfoComponent("subsystem_steps",
			"bucket_guard root not present (%s); monitor idle", bucketGuardRoot)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("subsystem_steps",
		"watching %s + %s", bucketGuardRoot, tcpLz4Root)

	ticker := time.NewTicker(subsystemStepsPollInterval)
	defer ticker.Stop()

	tickSubsystemSteps(bucketGuardRoot, tcpLz4Root)
	metrics.MarkMonitorTick("subsystem_steps")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickSubsystemSteps(bucketGuardRoot, tcpLz4Root)
			metrics.MarkMonitorTick("subsystem_steps")
		}
	}
}

func tickSubsystemSteps(bucketGuardRoot, tcpLz4Root string) {
	tickBucketGuard(bucketGuardRoot)
	tickTCPLz4Steps(tcpLz4Root)
}

func tickBucketGuard(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !bucketGuardSteps[name] {
			continue
		}
		datePath, err := latestDateFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		s, ok := readLastSummary(datePath)
		if !ok {
			continue
		}
		setBucketGuardQuantiles(name, s)
	}
}

func tickTCPLz4Steps(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !tcpLz4Steps[name] {
			continue
		}
		datePath, err := latestDateFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		s, ok := readLastSummary(datePath)
		if !ok {
			continue
		}
		// Split name "in_4001" → direction="in", port="4001".
		idx := strings.IndexByte(name, '_')
		if idx <= 0 || idx >= len(name)-1 {
			continue
		}
		direction := name[:idx]
		port := name[idx+1:]
		setLz4Quantiles(direction, port, s)
	}
}

func setBucketGuardQuantiles(step string, s latencySummary) {
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "p50").Set(s.Median)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "p90").Set(s.P90)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "p95").Set(s.P95)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "max").Set(s.Max)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "mean").Set(s.Mean)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "std_dev").Set(s.StdDev)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "work_frac").Set(s.WorkFrac)
}

func setLz4Quantiles(direction, port string, s latencySummary) {
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "p50").Set(s.Median)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "p90").Set(s.P90)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "p95").Set(s.P95)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "max").Set(s.Max)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "mean").Set(s.Mean)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "std_dev").Set(s.StdDev)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "work_frac").Set(s.WorkFrac)
}
