package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

var (
	meter api.Meter
)

// ProviderOwner owns the one shutdown/flush operation for the SDK meter
// provider created during startup. Shutdown is safe to call concurrently or
// repeatedly: the underlying provider is invoked exactly once and every
// caller observes the same result.
type ProviderOwner struct {
	once     sync.Once
	shutdown func(context.Context) error
	err      error
}

func newProviderOwner(shutdown func(context.Context) error) *ProviderOwner {
	return &ProviderOwner{shutdown: shutdown}
}

// Shutdown flushes and stops the owned provider exactly once. Callers must
// supply a fresh bounded context during process shutdown; the application
// context has already been cancelled by then.
func (o *ProviderOwner) Shutdown(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.once.Do(func() {
		if o.shutdown != nil {
			o.err = o.shutdown(ctx)
		}
	})
	return o.err
}

func getAllObservables() []api.Observable {
	return []api.Observable{
		// Core L1 metrics
		HLCoreBlockHeightGauge,
		HLCoreLatestBlockTimeGauge,
		HLCoreLastProcessedRound,
		HLCoreLastProcessedTime,

		// Metal (machine specific) metrics
		HLMetalParseDurationGauge,

		// Consensus metrics
		HLConsensusValidatorJailedStatus,
		HLConsensusValidatorStakeGauge,
		HLConsensusTotalStakeGauge,
		HLConsensusJailedStakeGauge,
		HLConsensusNotJailedStakeGauge,
		HLConsensusValidatorCountGauge,
		HLConsensusActiveStakeGauge,
		HLConsensusInactiveStakeGauge,
		HLConsensusValidatorActiveStatus,
		HLConsensusValidatorAPIActiveAndUnjailedStatus,
		HLConsensusAPIActiveAndUnjailedCountGauge,
		HLConsensusAPIActiveAndUnjailedStakeGauge,
		HLConsensusValidatorRTTGauge,

		// consensus monitoring metrics
		HLConsensusVoteRoundGauge,
		HLConsensusVoteTimeDiffGauge,
		HLConsensusCurrentRoundGauge,
		HLConsensusConnectivityGauge,
		HLConsensusDisconnectedSinceRoundGauge,
		HLConsensusHeartbeatStatusGauge,
		HLConsensusHeartbeatAckObservedGauge,
		HLConsensusQCParticipationGauge,
		HLConsensusRoundsPerBlockGauge,
		HLConsensusQCRoundLagGauge,

		// val latency metrics
		HLConsensusValidatorLatencyGauge,
		HLConsensusValidatorLatencyRoundGauge,
		HLConsensusValidatorLatencyEMAGauge,

		// P2P metrics (non validator peers)
		HLP2PNonValPeerConnectionsGauge,
		HLP2PNonValPeersTotalGauge,

		// hl-node client metrics
		HLSoftwareVersionInfo,
		HLSoftwareUpToDate,

		// EVM metrics
		HLEVMBlockHeightGauge,
		HLEVMLatestBlockTimeGauge,
		HLEVMBaseFeeGauge,
		HLEVMGasUsedGauge,
		HLEVMGasLimitGauge,
		HLEVMSGasUtilGauge,
		HLEVMMaxPriorityFeeGauge,

		// memory metrics
		HLGoHeapObjects,
		HLGoHeapInuseMB,
		HLGoHeapIdleMB,
		HLGoSysMB,
		HLGoNumGoroutines,

		// monitor health metrics
		HLConsensusMonitorLastProcessedGauge,
	}
}

// TODO
func getCommonLabels() []attribute.KeyValue {
	return []attribute.KeyValue{}
}
