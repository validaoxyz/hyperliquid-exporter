package monitors

import (
	"context"
	"errors"
	"fmt"
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

// consensusSteps is the validator-only allowlist of sub-steps inside the
// `consensus` subsystem. These are the timing breakdowns of how long the
// consensus state machine spent handling each input type — the cleanest
// view of where the validator's CPU is going in the consensus loop.
// Cardinality 19 × 7 quantiles = 133 series.
var consensusSteps = map[string]bool{
	"BlockGap":                             true,
	"ExpensiveStatus":                      true,
	"HandleAllStateInputs":                 true,
	"HandleStateInput::Block":              true,
	"HandleStateInput::BlocksAndTxs":       true,
	"HandleStateInput::EmptyBlockTimer":    true,
	"HandleStateInput::ExecutionState":     true,
	"HandleStateInput::Heartbeat":          true,
	"HandleStateInput::HeartbeatAck":       true,
	"HandleStateInput::JailVoteTimer":      true,
	"HandleStateInput::L1Hash":             true,
	"HandleStateInput::PeriodicVoteStream": true,
	"HandleStateInput::Tc":                 true,
	"HandleStateInput::Timeout":            true,
	"HandleStateInput::TriggerTimeout":     true,
	"HandleStateInput::Tx":                 true,
	"HandleStateInput::Vote":               true,
	"TxCommit":                             true,
	"WallClockBlockGap":                    true,
}

// l1TaskSteps covers the four L1 (HyperCore) block-apply phases.
var l1TaskSteps = map[string]bool{
	"BeginBlock":           true,
	"DeliverSignedActions": true,
	"EndBlock":             true,
	"RecoverUsers":         true,
}

const subsystemStepsPollInterval = 60 * time.Second

type stepMetricFamily uint8

const (
	stepFamilyBucketGuard stepMetricFamily = iota
	stepFamilyTCPLZ4
	stepFamilyConsensus
	stepFamilyL1Task
)

type stepMetricKey struct {
	family stepMetricFamily
	name   string
}

type subsystemStepsState struct {
	published map[stepMetricKey]struct{}
}

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
	root := filepath.Join(cfg.NodeHome, "data", "latency_summaries")
	logger.InfoComponent("subsystem_steps", "watching nested summaries under %s", root)
	metrics.RegisterSource(metrics.SourceSubsystemSteps, true)
	state := &subsystemStepsState{published: make(map[stepMetricKey]struct{})}

	ticker := time.NewTicker(subsystemStepsPollInterval)
	defer ticker.Stop()

	if err := tickSubsystemSteps(root, state, time.Now()); err != nil {
		logger.DebugComponent("subsystem_steps", "initial scan: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tickSubsystemSteps(root, state, time.Now()); err != nil {
				logger.DebugComponent("subsystem_steps", "scan: %v", err)
			}
		}
	}
}

func tickSubsystemSteps(root string, state *subsystemStepsState, now time.Time) error {
	metrics.MarkMonitorAttempt("subsystem_steps")
	metrics.MarkSourceAttempt(metrics.SourceSubsystemSteps)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			withdrawSubsystemSteps(state)
			metrics.MarkSourceAbsent(metrics.SourceSubsystemSteps)
			metrics.MarkMonitorPublication("subsystem_steps")
			return nil
		}
		metrics.MarkSourceError(metrics.SourceSubsystemSteps, metrics.SourceFailureStat)
		return fmt.Errorf("stat summaries root: %w", err)
	}

	snapshot := make(map[stepMetricKey]latencySummary)
	var newest time.Time
	scopes := []struct {
		dir       string
		family    stepMetricFamily
		allowlist map[string]bool
	}{
		{"bucket_guard", stepFamilyBucketGuard, bucketGuardSteps},
		{"tcp_lz4", stepFamilyTCPLZ4, tcpLz4Steps},
		{"consensus", stepFamilyConsensus, consensusSteps},
		{"l1_task_latency", stepFamilyL1Task, l1TaskSteps},
	}
	for _, scope := range scopes {
		t, err := scanSubsystemStepRoot(filepath.Join(root, scope.dir), scope.family, scope.allowlist, now, snapshot)
		if err != nil {
			metrics.MarkSourceError(metrics.SourceSubsystemSteps, metrics.SourceFailureRead)
			return fmt.Errorf("scan %s: %w", scope.dir, err)
		}
		if t.After(newest) {
			newest = t
		}
	}

	replaceSubsystemSteps(state, snapshot)
	if len(snapshot) == 0 {
		metrics.MarkSourceAvailable(metrics.SourceSubsystemSteps)
	} else {
		metrics.MarkSourceValidObservation(metrics.SourceSubsystemSteps, newest)
		metrics.MarkMonitorValidObservation("subsystem_steps")
	}
	metrics.MarkSourcePublication(metrics.SourceSubsystemSteps)
	metrics.MarkMonitorPublication("subsystem_steps")
	return nil
}

func scanSubsystemStepRoot(root string, family stepMetricFamily, allowlist map[string]bool, now time.Time, snapshot map[stepMetricKey]latencySummary) (time.Time, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var newest time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !allowlist[name] {
			continue
		}
		datePath, err := latestDateFile(filepath.Join(root, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return time.Time{}, err
		}
		s, sampleTime, err := readLastSummaryComplete(datePath)
		if err != nil {
			if errors.Is(err, errNoCompleteSummary) {
				continue
			}
			return time.Time{}, err
		}
		if now.Sub(sampleTime) > subsystemLatencyStaleAfter {
			continue
		}
		snapshot[stepMetricKey{family: family, name: name}] = s
		if sampleTime.After(newest) {
			newest = sampleTime
		}
	}
	return newest, nil
}

func setConsensusQuantiles(step string, s latencySummary) {
	metrics.HLLatencyConsensus.WithLabelValues(step, "p50").Set(s.Median)
	metrics.HLLatencyConsensus.WithLabelValues(step, "p90").Set(s.P90)
	metrics.HLLatencyConsensus.WithLabelValues(step, "p95").Set(s.P95)
	metrics.HLLatencyConsensus.WithLabelValues(step, "max").Set(s.Max)
	metrics.HLLatencyConsensus.WithLabelValues(step, "mean").Set(s.Mean)
	metrics.HLLatencyConsensus.WithLabelValues(step, "std_dev").Set(s.StdDev)
	metrics.HLLatencyConsensusWorkFraction.WithLabelValues(step).Set(s.WorkFrac)
}

func setL1TaskQuantiles(step string, s latencySummary) {
	metrics.HLLatencyL1Task.WithLabelValues(step, "p50").Set(s.Median)
	metrics.HLLatencyL1Task.WithLabelValues(step, "p90").Set(s.P90)
	metrics.HLLatencyL1Task.WithLabelValues(step, "p95").Set(s.P95)
	metrics.HLLatencyL1Task.WithLabelValues(step, "max").Set(s.Max)
	metrics.HLLatencyL1Task.WithLabelValues(step, "mean").Set(s.Mean)
	metrics.HLLatencyL1Task.WithLabelValues(step, "std_dev").Set(s.StdDev)
	metrics.HLLatencyL1TaskWorkFraction.WithLabelValues(step).Set(s.WorkFrac)
}

func setBucketGuardQuantiles(step string, s latencySummary) {
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "p50").Set(s.Median)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "p90").Set(s.P90)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "p95").Set(s.P95)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "max").Set(s.Max)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "mean").Set(s.Mean)
	metrics.HLLatencyBucketGuard.WithLabelValues(step, "std_dev").Set(s.StdDev)
	metrics.HLLatencyBucketGuardWorkFraction.WithLabelValues(step).Set(s.WorkFrac)
}

func setLz4Quantiles(direction, port string, s latencySummary) {
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "p50").Set(s.Median)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "p90").Set(s.P90)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "p95").Set(s.P95)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "max").Set(s.Max)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "mean").Set(s.Mean)
	metrics.HLTCPLz4Latency.WithLabelValues(direction, port, "std_dev").Set(s.StdDev)
	metrics.HLTCPLz4WorkFraction.WithLabelValues(direction, port).Set(s.WorkFrac)
}

func replaceSubsystemSteps(state *subsystemStepsState, snapshot map[stepMetricKey]latencySummary) {
	for key, summary := range snapshot {
		publishSubsystemStep(key, summary)
	}
	for key := range state.published {
		if _, ok := snapshot[key]; !ok {
			deleteSubsystemStep(key)
		}
	}
	state.published = make(map[stepMetricKey]struct{}, len(snapshot))
	for key := range snapshot {
		state.published[key] = struct{}{}
	}
}

func withdrawSubsystemSteps(state *subsystemStepsState) {
	for key := range state.published {
		deleteSubsystemStep(key)
	}
	state.published = make(map[stepMetricKey]struct{})
}

func publishSubsystemStep(key stepMetricKey, summary latencySummary) {
	switch key.family {
	case stepFamilyBucketGuard:
		setBucketGuardQuantiles(key.name, summary)
	case stepFamilyTCPLZ4:
		direction, port, ok := strings.Cut(key.name, "_")
		if ok {
			setLz4Quantiles(direction, port, summary)
		}
	case stepFamilyConsensus:
		setConsensusQuantiles(key.name, summary)
	case stepFamilyL1Task:
		setL1TaskQuantiles(key.name, summary)
	}
}

func deleteSubsystemStep(key stepMetricKey) {
	quantiles := []string{"p50", "p90", "p95", "max", "mean", "std_dev"}
	switch key.family {
	case stepFamilyBucketGuard:
		for _, quantile := range quantiles {
			metrics.HLLatencyBucketGuard.DeleteLabelValues(key.name, quantile)
		}
		metrics.HLLatencyBucketGuardWorkFraction.DeleteLabelValues(key.name)
	case stepFamilyTCPLZ4:
		direction, port, ok := strings.Cut(key.name, "_")
		if !ok {
			return
		}
		for _, quantile := range quantiles {
			metrics.HLTCPLz4Latency.DeleteLabelValues(direction, port, quantile)
		}
		metrics.HLTCPLz4WorkFraction.DeleteLabelValues(direction, port)
	case stepFamilyConsensus:
		for _, quantile := range quantiles {
			metrics.HLLatencyConsensus.DeleteLabelValues(key.name, quantile)
		}
		metrics.HLLatencyConsensusWorkFraction.DeleteLabelValues(key.name)
	case stepFamilyL1Task:
		for _, quantile := range quantiles {
			metrics.HLLatencyL1Task.DeleteLabelValues(key.name, quantile)
		}
		metrics.HLLatencyL1TaskWorkFraction.DeleteLabelValues(key.name)
	}
}
