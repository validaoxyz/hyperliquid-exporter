package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// This file holds bounded, domain-specific Prometheus instruments added by
// the validator/consensus audit. Keeping them out of the legacy omnibus file
// makes the source and label contracts reviewable as one unit.
var (
	HLConsensusBlockDirectionEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_block_direction_events_total",
		Help: "Block message events observed since exporter start by wire direction; duplicate out/in copies remain distinct here while block-level observations are deduplicated.",
	}, []string{"direction"})

	HLCoreBlockBeginWallReceiptLag = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hl_core_block_begin_wall_receipt_lag_seconds",
		Help:    "Nonnegative exporter receipt lag since begin_block_wall_time, by block-time source class; this is local wall-clock observation lag, not network propagation time.",
		Buckets: []float64{.001, .002, .005, .01, .02, .05, .1, .2, .3, .5, .75, 1, 2, 5, 10, 30, 60, 180},
	}, []string{"source_class"})

	HLConsensusRPCEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_rpc_events_total",
		Help: "Consensus RPC lifecycle events since exporter start, classified only into fixed direction, stage, outcome, and content vocabularies.",
	}, []string{"direction", "stage", "outcome", "content"})
	HLConsensusRPCBlocksServed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_rpc_blocks_served_total",
		Help: "Blocks explicitly reported sent by complete query_peers=false Outbound response Ok.BlocksAndTxs results since exporter start.",
	})
	HLConsensusRPCParse = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_rpc_parse_total",
		Help: "Consensus RPC source records since exporter start by bounded parser result.",
	}, []string{"result"})

	HLConsensusLocalRound = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_local_round",
		Help: "Latest fixture-proven local consensus-round value by fixed field; silence makes source age grow and does not imply jail or network lag.",
	}, []string{"field"})
	HLConsensusLocalRoundLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_local_round_lag",
		Help: "Nonnegative difference between same-domain local consensus round fields from fixture-proven events.",
	}, []string{"field"})
	HLConsensusRoundAdvanceEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_round_advance_events_total",
		Help: "Local round-advance events since exporter start by bounded reason.",
	}, []string{"reason"})
	HLConsensusAcceptedVoteObservations = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_accepted_vote_observations_total",
		Help: "Accepted vote observations since exporter start; peer votes are leadership-sampled and this counter is not a committee-coverage denominator.",
	})

	HLConsensusStatusFieldReported = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_status_field_reported",
		Help: "Whether a fixed nested field was present in the latest complete status snapshot (1=present, 0=omitted).",
	}, []string{"field"})
	HLConsensusStatusEligibleSummary = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_status_api_active_and_unjailed_validators",
		Help: "Count from the latest complete status snapshot joined strictly to the latest API active-and-unjailed set; missing/null is never synthesized as zero.",
	}, []string{"state"})

	HLConsensusHeartbeatPeerAcks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_heartbeat_peer_acks_total",
		Help: "Exact non-self heartbeat acknowledgements joined since exporter start by canonical validator identity.",
	}, []string{"validator", "signer", "name"})
	HLConsensusHeartbeatPeerAckDelay = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hl_consensus_heartbeat_peer_ack_delay_seconds",
		Help:    "Delay in seconds for exact non-self heartbeat acknowledgement joins; local loopback is excluded.",
		Buckets: prometheus.ExponentialBuckets(.0001, 2, 18),
	})
	HLConsensusHeartbeatJoin = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_heartbeat_ack_join_total",
		Help: "Heartbeat acknowledgement joins since exporter start by fixed kind and outcome; random identifiers are never labels.",
	}, []string{"kind", "outcome"})
	HLConsensusSelfHeartbeatLoopDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hl_consensus_self_heartbeat_loop_duration_seconds",
		Help:    "Duration in seconds of exact local heartbeat loopback joins, kept separate from peer latency.",
		Buckets: prometheus.ExponentialBuckets(.000001, 2, 18),
	})

	HLConsensusValidatorLatencyEMAState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_latency_ema_state",
		Help: "One-hot state of the latest EMA generation: measured, initializing, no_data, or invalid.",
	}, []string{"state"})

	HLConsensusValidatorTCPConnectDuration = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_tcp_connect_duration_seconds",
		Help: "Most recent successful TCP connect duration in seconds for a fresh API-active-and-unjailed validator target; not protocol RTT.",
	}, []string{"validator", "signer", "name"})
	HLConsensusValidatorTCPConnectLastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_tcp_connect_last_success_timestamp_seconds",
		Help: "Unix timestamp of the most recent successful TCP connect to a fresh API-active-and-unjailed validator target.",
	}, []string{"validator", "signer", "name"})
	HLConsensusValidatorTCPConnectSuccessAge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_tcp_connect_last_success_age_seconds",
		Help: "Seconds since the most recent successful TCP connect to a fresh API-active-and-unjailed validator target.",
	}, []string{"validator", "signer", "name"})
	HLConsensusValidatorTCPConnectOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_consensus_validator_tcp_connect_attempts_total",
		Help: "TCP connect probe attempts since exporter start by canonical validator identity and bounded outcome.",
	}, []string{"validator", "signer", "name", "outcome"})

	HLValidatorConnectionEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_validator_connection_events_total",
		Help: "Sparse validator-subsystem connection events since exporter start using fixed event, result, and subsystem classes; no endpoint or session labels.",
	}, []string{"event", "result", "subsystem"})
	HLValidatorConnectionParse = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_validator_connection_parse_total",
		Help: "Validator-connection source records since exporter start by bounded parser result.",
	}, []string{"result"})

	HLVisorHardforkVersion = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_visor_hardfork_version",
		Help: "Hardfork version reported by the latest valid visor state; this is not artifact hf or binary selection.",
	}, []string{"source"})
	HLVisorHardforkVersionAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_hardfork_version_available",
		Help: "Whether hardfork_version was present and nonnegative in the latest valid visor state.",
	})
	HLVisorScheduledFreezeHeightAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_scheduled_freeze_height_available",
		Help: "Whether scheduled_freeze_height was non-null in the latest valid visor state; the legacy height gauge is meaningful only while this is 1.",
	})
	HLVisorScheduledFreezeHeightCurrent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_visor_scheduled_freeze_height_current",
		Help: "Explicitly scheduled core/ABCI freeze height from the latest valid visor state; the series is absent while scheduled_freeze_height is null.",
	}, []string{"source"})

	HLNodePersistedABCIHeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_persisted_abci_height",
		Help: "Core/ABCI height read from the exact persisted checkpoint-height file for a fixed source class; not EVM block height or current execution head.",
	}, []string{"source_class"})
	HLNodePersistedABCIHeightGap = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_persisted_abci_height_gap",
		Help: "Difference between the fast and slow persisted core/ABCI checkpoint-height files when both are present in the same poll.",
	}, []string{"comparison"})
	HLNodePersistedFreezeABCIHeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_persisted_freeze_abci_height",
		Help: "Core/ABCI height read from the persisted freeze_abci_height file; persistence across process restarts does not make it a current scheduled freeze.",
	}, []string{"source"})
	HLNodeVisorHeightAbovePersistedFreeze = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_visor_height_above_persisted_freeze",
		Help: "Nonnegative latest visor height minus the persisted freeze_abci_height file value when both are available; not proof of a current scheduled freeze.",
	}, []string{"comparison"})
	HLNodePersistedStateFileAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_persisted_state_file_available",
		Help: "Whether a fixed persisted node-state file was present, readable, and held one nonnegative integer in the latest poll.",
	}, []string{"file"})
)

func SetValidatorLatencyEMAState(state string) {
	for _, candidate := range []string{"measured", "initializing", "no_data", "invalid"} {
		value := 0.0
		if candidate == state {
			value = 1
		}
		HLConsensusValidatorLatencyEMAState.WithLabelValues(candidate).Set(value)
	}
}

func SetTCPConnectSuccess(identity ValidatorIdentity, duration time.Duration, at time.Time) {
	labels := []string{identity.Validator, identity.Signer, normalizedValidatorName(identity.Name)}
	HLConsensusValidatorTCPConnectDuration.WithLabelValues(labels...).Set(duration.Seconds())
	HLConsensusValidatorTCPConnectLastSuccess.WithLabelValues(labels...).Set(float64(at.Unix()))
	HLConsensusValidatorTCPConnectSuccessAge.WithLabelValues(labels...).Set(0)
}

func SetTCPConnectSuccessAge(identity ValidatorIdentity, age time.Duration, currentAvailable bool) {
	labels := []string{identity.Validator, identity.Signer, normalizedValidatorName(identity.Name)}
	if age < 0 {
		age = 0
	}
	HLConsensusValidatorTCPConnectSuccessAge.WithLabelValues(labels...).Set(age.Seconds())
	if !currentAvailable {
		HLConsensusValidatorTCPConnectDuration.DeleteLabelValues(labels...)
	}
}

func DeleteTCPConnectTarget(identity ValidatorIdentity) {
	labels := []string{identity.Validator, identity.Signer, normalizedValidatorName(identity.Name)}
	HLConsensusValidatorTCPConnectDuration.DeleteLabelValues(labels...)
	HLConsensusValidatorTCPConnectLastSuccess.DeleteLabelValues(labels...)
	HLConsensusValidatorTCPConnectSuccessAge.DeleteLabelValues(labels...)
}
