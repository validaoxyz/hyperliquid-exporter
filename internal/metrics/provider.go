package metrics

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	api "go.opentelemetry.io/otel/metric"
)

var (
	meter metric.Meter
)

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
		HLConsensusValidatorRTTGauge,

		// consensus monitoring metrics
		HLConsensusVoteRoundGauge,
		HLConsensusVoteTimeDiffGauge,
		HLConsensusCurrentRoundGauge,
		HLConsensusConnectivityGauge,
		HLConsensusHeartbeatStatusGauge,
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
