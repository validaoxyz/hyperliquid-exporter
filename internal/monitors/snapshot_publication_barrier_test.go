package monitors

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	hyperliquidapi "github.com/validaoxyz/hyperliquid-exporter/internal/hyperliquid-api"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/replica"
	"go.opentelemetry.io/otel"
)

type lifecycleBlockingGatherer struct {
	entered chan struct{}
	release chan struct{}
}

func (g *lifecycleBlockingGatherer) Gather() ([]*dto.MetricFamily, error) {
	close(g.entered)
	<-g.release
	return nil, nil
}

func assertUsesSnapshotBarrier(t *testing.T, publish func()) {
	t.Helper()
	g := &lifecycleBlockingGatherer{entered: make(chan struct{}), release: make(chan struct{})}
	gatherDone := make(chan struct{})
	go func() {
		_, _ = metrics.ProtectPrometheusSnapshots(g).Gather()
		close(gatherDone)
	}()
	select {
	case <-g.entered:
	case <-time.After(time.Second):
		t.Fatal("protected gatherer did not enter")
	}

	publishDone := make(chan struct{})
	go func() {
		publish()
		close(publishDone)
	}()
	select {
	case <-publishDone:
		close(g.release)
		<-gatherDone
		t.Fatal("logical generation bypassed the scrape barrier")
	case <-time.After(25 * time.Millisecond):
	}

	close(g.release)
	select {
	case <-gatherDone:
	case <-time.After(time.Second):
		t.Fatal("protected gatherer did not release")
	}
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("logical generation did not publish after scrape released")
	}
}

func TestCorrectedLogicalSourcesUseSnapshotBarrier(t *testing.T) {
	t.Run("disk", func(t *testing.T) {
		snapshot := diskSnapshot{
			apparentByPath:  map[string]int64{},
			allocatedByPath: map[string]int64{},
			pathState:       map[string]string{},
		}
		assertUsesSnapshotBarrier(t, func() {
			tickDiskWith("unused",
				func(string) (*fsStats, error) { return &fsStats{Bavail: 1, Blocks: 2, Bsize: 4096}, nil },
				func(string, []string) (diskSnapshot, error) { return snapshot, nil },
				func() time.Time { return time.Unix(100, 0) },
			)
		})
	})

	t.Run("snapshot_status", func(t *testing.T) {
		assertUsesSnapshotBarrier(t, func() {
			commitSnapshotStatus(snapshotStatusSnapshot{}, time.Unix(100, 0), 0)
		})
	})

	t.Run("replay", func(t *testing.T) {
		assertUsesSnapshotBarrier(t, func() { commitReplaySnapshot(replaySnapshot{}) })
	})

	t.Run("replica_runs", func(t *testing.T) {
		assertUsesSnapshotBarrier(t, func() { commitReplicaRunsSnapshot(replicaRunsSnapshot{}) })
	})

	t.Run("public_ip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "last_known_public_ip.json")
		if err := os.WriteFile(path, []byte(`"203.0.113.9"`), 0o600); err != nil {
			t.Fatal(err)
		}
		current := ""
		assertUsesSnapshotBarrier(t, func() {
			_ = tickPublicIP(path, &current, time.Now())
		})
	})

	t.Run("operator_config", func(t *testing.T) {
		root := t.TempDir()
		assertUsesSnapshotBarrier(t, func() { tickOperatorConfig(root) })
	})

	t.Run("rocksdb", func(t *testing.T) {
		root := t.TempDir()
		assertUsesSnapshotBarrier(t, func() {
			tickRocksDB(root, newRocksDBMonitorState(), time.Unix(100, 0))
		})
	})

	t.Run("validator_status", func(t *testing.T) {
		line := `["2026-08-08T00:00:00.000000000",{"home_validator":"0x2222222222222222222222222222222222222222","round":1,"current_stakes":[],"current_jailed_validators":[]}]`
		assertUsesSnapshotBarrier(t, func() {
			if err := processValidatorStatusLine(line); err != nil {
				t.Errorf("process validator status: %v", err)
			}
		})
	})

	t.Run("consensus_status", func(t *testing.T) {
		monitor := NewConsensusMonitor(&config.Config{})
		line := `["2026-08-08T00:00:00.000000000",{"round":1,"heartbeat_statuses":[],"disconnected_validators":[]}]`
		assertUsesSnapshotBarrier(t, func() {
			if err := monitor.processStatusLine(line); err != nil {
				t.Errorf("process consensus status: %v", err)
			}
		})
	})

	t.Run("evm", func(t *testing.T) {
		processor := newEVMProcessor(newRecordingEVMSink(), false, 0)
		line := evmFixtureLine("2026-08-08T03:15:14.100000000", "0x2a", false, `[]`)
		assertUsesSnapshotBarrier(t, func() {
			if err := processor.processLine(line); err != nil {
				t.Errorf("process EVM line: %v", err)
			}
		})
	})

	t.Run("replica", func(t *testing.T) {
		counter, err := otel.Meter("replica-barrier-test").Int64Counter("test_replica_barrier_blocks")
		if err != nil {
			t.Fatal(err)
		}
		oldCounter := metrics.HLCoreBlocksProcessedCounter
		metrics.HLCoreBlocksProcessedCounter = counter
		t.Cleanup(func() { metrics.HLCoreBlocksProcessedCounter = oldCounter })
		monitor := NewReplicaMonitor("", 1)
		block := &replica.BlockMetrics{
			Height:          1,
			Round:           2,
			Time:            time.Unix(100, 0),
			ActionCounts:    map[string]int{},
			OperationCounts: map[string]int{},
			MultiSigInner:   map[string]int{},
			ParserEvents:    map[string]int{},
			Responses: replica.ResponseMetrics{
				Coverage:       "unavailable",
				CountRelation:  "equal",
				ActionStatuses: map[string]int{},
				Outcomes:       map[string]int{},
			},
		}
		assertUsesSnapshotBarrier(t, func() { monitor.commitBlock(block) })
	})

	t.Run("evm_withdrawal", func(t *testing.T) {
		processor := newEVMProcessor(newRecordingEVMSink(), false, 0)
		assertUsesSnapshotBarrier(t, func() { processor.commitUnavailable(tailStreamUnavailableEmpty) })
	})

	t.Run("replica_withdrawal", func(t *testing.T) {
		monitor := NewReplicaMonitor("", 1)
		assertUsesSnapshotBarrier(t, func() { monitor.commitUnavailable(tailStreamUnavailableEmpty) })
	})

	t.Run("info_meta", func(t *testing.T) {
		assertUsesSnapshotBarrier(t, func() {
			recordMetaFailure(metrics.InfoProbeOutcomeDecode, metrics.SourceFailureDecode, time.Unix(100, 0))
		})
	})

	t.Run("info_exchange", func(t *testing.T) {
		assertUsesSnapshotBarrier(t, func() {
			recordExchangeFailure(metrics.InfoProbeOutcomeDecode, metrics.SourceFailureDecode, time.Unix(100, 0))
		})
	})

	t.Run("validator_api", func(t *testing.T) {
		assertUsesSnapshotBarrier(t, func() {
			commitValidatorAPISnapshot([]hyperliquidapi.ValidatorSummary{}, []metrics.ValidatorSummarySnapshot{}, time.Unix(100, 0))
		})
	})
}
