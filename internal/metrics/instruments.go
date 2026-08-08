package metrics

import (
	"fmt"

	api "go.opentelemetry.io/otel/metric"
)

// metric instruments for hl_exporter
var (
	// counters consensus
	HLConsensusProposerCounter api.Int64Counter

	// counters Core
	HLCoreTxCounter              api.Int64Counter
	HLCoreOrdersCounter          api.Int64Counter
	HLCoreOperationsCounter      api.Int64Counter
	HLCoreBlocksProcessedCounter api.Int64Counter

	// counters EVM
	HLEVMTxTypeCounter api.Int64Counter

	// observable gauges, Core
	HLCoreBlockHeightGauge     api.Int64ObservableGauge
	HLCoreLatestBlockTimeGauge api.Int64ObservableGauge
	HLCoreLastProcessedRound   api.Int64ObservableGauge
	HLCoreLastProcessedTime    api.Int64ObservableGauge

	// observable gauges, Metal (machine specific)
	HLMetalParseDurationGauge api.Float64ObservableGauge

	// observable gauges, Consensus
	HLConsensusValidatorJailedStatus               api.Float64ObservableGauge
	HLConsensusValidatorStakeGauge                 api.Float64ObservableGauge
	HLConsensusTotalStakeGauge                     api.Float64ObservableGauge
	HLConsensusJailedStakeGauge                    api.Float64ObservableGauge
	HLConsensusNotJailedStakeGauge                 api.Float64ObservableGauge
	HLConsensusValidatorCountGauge                 api.Int64ObservableGauge
	HLConsensusActiveStakeGauge                    api.Float64ObservableGauge
	HLConsensusInactiveStakeGauge                  api.Float64ObservableGauge
	HLConsensusValidatorActiveStatus               api.Float64ObservableGauge
	HLConsensusValidatorAPIActiveAndUnjailedStatus api.Float64ObservableGauge
	HLConsensusAPIActiveAndUnjailedCountGauge      api.Int64ObservableGauge
	HLConsensusAPIActiveAndUnjailedStakeGauge      api.Float64ObservableGauge
	HLConsensusValidatorRTTGauge                   api.Float64ObservableGauge

	// observable gauges, hl-node client
	HLSoftwareVersionInfo api.Int64ObservableGauge
	HLSoftwareUpToDate    api.Int64ObservableGauge

	// observable gauges, EVM
	HLEVMBlockHeightGauge     api.Int64ObservableGauge
	HLEVMLatestBlockTimeGauge api.Int64ObservableGauge
	HLEVMBaseFeeGauge         api.Float64ObservableGauge
	HLEVMGasUsedGauge         api.Float64ObservableGauge
	HLEVMGasLimitGauge        api.Float64ObservableGauge
	HLEVMSGasUtilGauge        api.Float64ObservableGauge
	HLEVMMaxPriorityFeeGauge  api.Float64ObservableGauge

	// histograms, Core
	HLCoreBlockTimeHistogram          api.Float64Histogram
	HLCoreTxPerBlockHistogram         api.Float64Histogram
	HLCoreOrdersPerBlockHistogram     api.Float64Histogram
	HLCoreOperationsPerBlockHistogram api.Float64Histogram
	HLMetalApplyDurationHistogram     api.Float64Histogram

	// histograms, EVM
	HLEVMBlockTimeHistogram  api.Float64Histogram
	HLEVMTxPerBlockHistogram api.Float64Histogram

	// histograms, EVM fees
	HLEVMBaseFeeHistogram     api.Float64Histogram
	HLEVMPriorityFeeHistogram api.Float64Histogram

	// consensus monitoring metrics
	HLConsensusVoteRoundGauge              api.Int64ObservableGauge
	HLConsensusVoteTimeDiffGauge           api.Float64ObservableGauge
	HLConsensusCurrentRoundGauge           api.Int64ObservableGauge
	HLConsensusHeartbeatSentCounter        api.Int64Counter
	HLConsensusConnectivityGauge           api.Float64ObservableGauge
	HLConsensusDisconnectedSinceRoundGauge api.Int64ObservableGauge
	HLConsensusHeartbeatStatusGauge        api.Float64ObservableGauge
	HLConsensusHeartbeatAckObservedGauge   api.Float64ObservableGauge

	// QC and TC metrics
	HLConsensusQCSignaturesCounter    api.Int64Counter
	HLConsensusQCParticipationGauge   api.Float64ObservableGauge
	HLConsensusQCSizeHist             api.Float64Histogram
	HLConsensusTCBlocksCounter        api.Int64Counter
	HLConsensusTCParticipationCounter api.Int64Counter
	HLConsensusTCSizeHist             api.Float64Histogram

	HLConsensusRoundsPerBlockGauge api.Float64ObservableGauge
	HLConsensusQCRoundLagGauge     api.Float64ObservableGauge

	// validator latency metrics (part of consensus metrics)
	HLConsensusValidatorLatencyGauge      api.Float64ObservableGauge
	HLConsensusValidatorLatencyRoundGauge api.Int64ObservableGauge
	HLConsensusValidatorLatencyEMAGauge   api.Float64ObservableGauge

	// P2P metrics (non-validator peers)
	HLP2PNonValPeerConnectionsGauge api.Float64ObservableGauge
	HLP2PNonValPeersTotalGauge      api.Float64ObservableGauge

	// monitor health metrics
	HLConsensusMonitorLastProcessedGauge api.Int64ObservableGauge
	HLConsensusMonitorLinesCounter       api.Int64Counter
	HLConsensusMonitorErrorsCounter      api.Int64Counter
)

func createInstruments() error {
	var err error

	blockTimeBuckets := []float64{
		10, 20, 30, 40, 50, 60, 70, 80, 90, 100,
		120, 140, 160, 180, 200, 220, 240, 260, 280, 300,
		350, 400, 450, 500, 600, 700, 800,
		900, 1000, 1500, 2000,
	}

	HLCoreBlockTimeHistogram, err = meter.Float64Histogram(
		"hl_core_block_time_milliseconds",
		api.WithDescription("Distribution of time between blocks in milliseconds"),
		api.WithUnit("ms"),
		api.WithExplicitBucketBoundaries(blockTimeBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create core block time histogram: %w", err)
	}

	applyDurationBuckets := []float64{
		0.1, 0.2, 0.5, 1, 2, 3, 5, 7, 10, 15, 20,
		30, 50, 75, 100, 150, 160, 170, 180, 190,
		200, 210, 220, 230, 240, 250,
		300, 400, 500, 750, 1000, 1500, 2000, 3000, 5000, 10000,
	}

	HLMetalApplyDurationHistogram, err = meter.Float64Histogram(
		"hl_metal_apply_duration_milliseconds",
		api.WithDescription("Distribution of block apply durations in milliseconds"),
		api.WithUnit("ms"),
		api.WithExplicitBucketBoundaries(applyDurationBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create metal apply duration histogram: %w", err)
	}

	HLConsensusProposerCounter, err = meter.Int64Counter(
		"hl_consensus_proposer_count_total",
		api.WithDescription("Total number of blocks proposed by each validator"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus proposer counter: %w", err)
	}

	HLCoreBlockHeightGauge, err = meter.Int64ObservableGauge(
		"hl_core_block_height",
		api.WithDescription("Current block height of the chain"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core block height gauge: %w", err)
	}

	HLConsensusValidatorJailedStatus, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_jailed_status",
		api.WithDescription("Raw isJailed from the latest complete validatorSummaries response (1=jailed, 0=not jailed)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus validator jailed status gauge: %w", err)
	}

	HLConsensusValidatorStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_stake",
		api.WithDescription("Raw validatorSummaries stake for each validator in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus validator stake gauge: %w", err)
	}

	HLConsensusTotalStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_total_stake",
		api.WithDescription("Sum of raw validatorSummaries stake in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus total stake gauge: %w", err)
	}

	HLConsensusJailedStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_jailed_stake",
		api.WithDescription("Raw validatorSummaries stake summed over isJailed rows, in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus jailed stake gauge: %w", err)
	}

	HLConsensusNotJailedStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_not_jailed_stake",
		api.WithDescription("Raw validatorSummaries stake summed over non-jailed rows, in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus not jailed stake gauge: %w", err)
	}

	HLConsensusValidatorCountGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_validators",
		api.WithDescription("Number of rows in the latest complete validatorSummaries response; not a committee count"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus validator count gauge: %w", err)
	}

	HLSoftwareVersionInfo, err = meter.Int64ObservableGauge(
		"hl_software_version",
		api.WithDescription("Software version information"),
	)
	if err != nil {
		return fmt.Errorf("failed to create software version info gauge: %w", err)
	}

	HLSoftwareUpToDate, err = meter.Int64ObservableGauge(
		"hl_software_up_to_date",
		api.WithDescription("Software up to date status"),
	)
	if err != nil {
		return fmt.Errorf("failed to create software up to date gauge: %w", err)
	}

	HLCoreLatestBlockTimeGauge, err = meter.Int64ObservableGauge(
		"hl_core_latest_block_time",
		api.WithDescription("Latest block time"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core latest block time gauge: %w", err)
	}

	HLEVMBlockHeightGauge, err = meter.Int64ObservableGauge(
		"hl_evm_block_height",
		api.WithDescription("Current block height of the EVM chain"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM block height gauge: %w", err)
	}

	HLEVMTxTypeCounter, err = meter.Int64Counter(
		"hl_evm_tx_type_total",
		api.WithDescription("Total number of EVM transactions by type"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM transaction type counter: %w", err)
	}

	HLCoreTxCounter, err = meter.Int64Counter(
		"hl_core_tx_total",
		api.WithDescription("Validated signed actions observed in replica_cmds, by closed action type; compatibility name retains tx"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core transaction counter: %w", err)
	}

	HLCoreOrdersCounter, err = meter.Int64Counter(
		"hl_core_orders_total",
		api.WithDescription("Individual order operations inside validated replica order and twapOrder actions"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core order counter: %w", err)
	}

	HLCoreOperationsCounter, err = meter.Int64Counter(
		"hl_core_operations_total",
		api.WithDescription("Individual operations inside validated replica signed actions (array items for orders, cancels, and batch modifications)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core operations counter: %w", err)
	}

	HLCoreBlocksProcessedCounter, err = meter.Int64Counter(
		"hl_core_blocks_processed",
		api.WithDescription("Completely validated replica_cmds block records observed since exporter start"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core blocks processed counter: %w", err)
	}

	// count-oriented buckets for per-block core load. Operations can be
	// materially larger than actions because one action may expand into many
	// internal operations, so keep a wider tail for that histogram.
	coreTxPerBlockBuckets := []float64{
		0, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000,
	}
	coreOrdersPerBlockBuckets := []float64{
		0, 1, 2, 5, 10, 20, 50, 100, 150, 200, 250, 300,
		400, 500, 750, 1000, 1500, 2000, 3000, 5000, 7500, 10000,
	}
	coreOperationsPerBlockBuckets := []float64{
		0, 1, 2, 5, 10, 20, 50, 100, 200, 500,
		1000, 2000, 3000, 5000, 7500, 10000, 15000, 20000, 30000, 50000,
	}
	HLCoreTxPerBlockHistogram, err = meter.Float64Histogram(
		"hl_core_tx_per_block",
		api.WithDescription("Distribution of validated signed actions per replica block; compatibility name retains tx"),
		api.WithUnit("actions"),
		api.WithExplicitBucketBoundaries(coreTxPerBlockBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create core transactions per block histogram: %w", err)
	}

	HLCoreOrdersPerBlockHistogram, err = meter.Float64Histogram(
		"hl_core_orders_per_block",
		api.WithDescription("Distribution of individual order operations per validated replica block"),
		api.WithUnit("orders"),
		api.WithExplicitBucketBoundaries(coreOrdersPerBlockBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create core orders per block histogram: %w", err)
	}

	HLCoreOperationsPerBlockHistogram, err = meter.Float64Histogram(
		"hl_core_operations_per_block",
		api.WithDescription("Distribution of individual operations per block in the Hyperliquid network"),
		api.WithUnit("operations"),
		api.WithExplicitBucketBoundaries(coreOperationsPerBlockBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create core operations per block histogram: %w", err)
	}

	HLCoreLastProcessedRound, err = meter.Int64ObservableGauge(
		"hl_core_last_processed_round",
		api.WithDescription("Last processed round in the Hyperliquid network"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core last processed round gauge: %w", err)
	}

	HLCoreLastProcessedTime, err = meter.Int64ObservableGauge(
		"hl_core_last_processed_time",
		api.WithDescription("Last processed time in the Hyperliquid network"),
	)
	if err != nil {
		return fmt.Errorf("failed to create core last processed time gauge: %w", err)
	}

	// implementation-specific metrics
	HLMetalParseDurationGauge, err = meter.Float64ObservableGauge(
		"hl_metal_parse_duration",
		api.WithDescription("Duration of replica transaction parsing"),
	)
	if err != nil {
		return fmt.Errorf("failed to create metal parse duration gauge: %w", err)
	}

	// replica commands metrics
	HLConsensusActiveStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_active_stake",
		api.WithDescription("Raw validatorSummaries stake summed over isActive rows, including jailed rows, in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus active stake gauge: %w", err)
	}

	HLConsensusInactiveStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_inactive_stake",
		api.WithDescription("Raw validatorSummaries stake summed over non-active rows, in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus inactive stake gauge: %w", err)
	}

	HLConsensusValidatorActiveStatus, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_active_status",
		api.WithDescription("Raw isActive from the latest complete validatorSummaries response (1=active, 0=inactive); not committee membership"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus validator active status gauge: %w", err)
	}

	HLConsensusValidatorAPIActiveAndUnjailedStatus, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_api_active_and_unjailed_status",
		api.WithDescription("Whether the latest complete validatorSummaries row is API-active and not jailed (1=yes, 0=no); this is not committee membership"),
	)
	if err != nil {
		return fmt.Errorf("failed to create API-active-and-unjailed validator gauge: %w", err)
	}

	HLConsensusAPIActiveAndUnjailedCountGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_api_active_and_unjailed_validators",
		api.WithDescription("Number of validators both active and not jailed in the latest complete validatorSummaries response; not a committee count"),
	)
	if err != nil {
		return fmt.Errorf("failed to create API-active-and-unjailed validator count gauge: %w", err)
	}

	HLConsensusAPIActiveAndUnjailedStakeGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_api_active_and_unjailed_stake",
		api.WithDescription("Raw validatorSummaries stake summed over rows that are both API-active and not jailed, in 1e-8 HYPE units"),
	)
	if err != nil {
		return fmt.Errorf("failed to create API-active-and-unjailed stake gauge: %w", err)
	}

	HLConsensusValidatorRTTGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_rtt",
		api.WithDescription("Deprecated compatibility family: most recent successful TCP connect duration to a validator endpoint in milliseconds; not protocol RTT"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus validator RTT gauge: %w", err)
	}

	// histograms buckets list for EVM block time monitors
	evmBlockTimeBuckets := []float64{
		200, 400, 600, 800, 900, 1000, 1100, 1200, 1300, 1400,
		1500, 1600, 1700, 1800, 1900, 2000, 2200, 2500, 2800, 3200,
	}
	HLEVMBlockTimeHistogram, err = meter.Float64Histogram(
		"hl_evm_block_time_milliseconds",
		api.WithDescription("Distribution of EVM block time in milliseconds"),
		api.WithUnit("ms"),
		api.WithExplicitBucketBoundaries(evmBlockTimeBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM block time histogram: %w", err)
	}

	HLEVMLatestBlockTimeGauge, err = meter.Int64ObservableGauge(
		"hl_evm_latest_block_time",
		api.WithDescription("Latest EVM block time"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM latest block time gauge: %w", err)
	}

	// EVM base fee gauge (gwei)
	HLEVMBaseFeeGauge, err = meter.Float64ObservableGauge(
		"hl_evm_base_fee_gwei",
		api.WithDescription("Base fee per gas (Gwei) from latest EVM block header"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM base fee gauge: %w", err)
	}

	// EVM resource usage
	HLEVMGasUsedGauge, err = meter.Float64ObservableGauge(
		"hl_evm_gas_used",
		api.WithDescription("Gas used in EVM transactions"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM gas used gauge: %w", err)
	}

	HLEVMGasLimitGauge, err = meter.Float64ObservableGauge(
		"hl_evm_gas_limit",
		api.WithDescription("Gas limit in EVM transactions"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM gas limit gauge: %w", err)
	}

	HLEVMSGasUtilGauge, err = meter.Float64ObservableGauge(
		"hl_evm_gas_util",
		api.WithDescription("Gas utilization in EVM transactions"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM gas utilization gauge: %w", err)
	}

	HLEVMMaxPriorityFeeGauge, err = meter.Float64ObservableGauge(
		"hl_evm_max_priority_fee_gwei",
		api.WithDescription("Maximum priority fee in EVM transactions"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM max priority fee gauge: %w", err)
	}

	// fine-grained buckets for EVM transactions per block
	evmTxPerBlockBuckets := []float64{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 15, 20, 25, 30, 35, 40,
	}
	HLEVMTxPerBlockHistogram, err = meter.Float64Histogram(
		"hl_evm_tx_per_block",
		api.WithDescription("Distribution of EVM transactions per block"),
		api.WithUnit("transactions"),
		api.WithExplicitBucketBoundaries(evmTxPerBlockBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM transactions per block histogram: %w", err)
	}

	// histograms for fee tracking
	HLEVMBaseFeeHistogram, err = meter.Float64Histogram(
		"hl_evm_base_fee_gwei_distribution",
		api.WithDescription("Distribution of EVM base fee (Gwei)"),
		api.WithUnit("gwei"),
		api.WithExplicitBucketBoundaries(
			0.1, 0.5, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
			15, 20, 25, 30, 40, 50, 60, 70, 80, 90, 100,
			125, 150, 175, 200, 250, 300, 400, 500, 600, 700, 800, 900, 1000,
			// overflow for extreme outliers
			1500, 2000, 5000,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM base fee histogram: %w", err)
	}

	HLEVMPriorityFeeHistogram, err = meter.Float64Histogram(
		"hl_evm_max_priority_fee_gwei_distribution",
		api.WithDescription("Distribution of EVM max priority fee (Gwei)"),
		api.WithUnit("gwei"),
		api.WithExplicitBucketBoundaries(
			0.1, 0.5, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
			15, 20, 25, 30, 40, 50, 60, 70, 80, 90, 100,
			125, 150, 175, 200, 250, 300, 400, 500, 600, 700, 800, 900, 1000,
			// owerflow
			1500, 2000, 5000,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM priority fee histogram: %w", err)
	}

	// init memory monitoring metrics
	if err := InitMemoryMetrics(meter); err != nil {
		return fmt.Errorf("failed to initialize memory metrics: %w", err)
	}

	HLConsensusVoteRoundGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_vote_round",
		api.WithDescription("Last observed vote round per validator; peer votes are leadership-sampled when this node is next proposer, not continuous coverage"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus vote round gauge: %w", err)
	}

	HLConsensusVoteTimeDiffGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_vote_time_diff_seconds",
		api.WithDescription("Age of the last observed vote per validator; peer observations are leadership-sampled and not a continuous liveness denominator"),
		api.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus vote time diff gauge: %w", err)
	}

	HLConsensusCurrentRoundGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_current_round",
		api.WithDescription("Latest local consensus round observed from a fixture-proven round-advance, status, or block event"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus current round gauge: %w", err)
	}

	HLConsensusHeartbeatSentCounter, err = meter.Int64Counter(
		"hl_consensus_heartbeat_sent_total",
		api.WithDescription("Total heartbeats sent by validators (validator nodes only)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus heartbeat sent counter: %w", err)
	}

	HLConsensusConnectivityGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_connectivity",
		api.WithDescription("Value 0 for each subject and reporter relation in the latest complete disconnected_validators status snapshot; absence is not connected=1"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus connectivity gauge: %w", err)
	}

	HLConsensusDisconnectedSinceRoundGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_validator_disconnected_since_round",
		api.WithDescription("Consensus round at which a status reporter began reporting a signer as disconnected, from the latest complete status snapshot"),
	)
	if err != nil {
		return fmt.Errorf("failed to create disconnected-since-round gauge: %w", err)
	}

	HLConsensusHeartbeatStatusGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_heartbeat_status",
		api.WithDescription("Seconds-valued heartbeat fields from the latest complete status snapshot; status_type is since_last_success or last_ack_duration"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus heartbeat status gauge: %w", err)
	}

	HLConsensusHeartbeatAckObservedGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_heartbeat_ack_observed",
		api.WithDescription("Whether last_ack_duration was numeric in the latest complete status snapshot (1=numeric including zero, 0=explicit null)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create heartbeat acknowledgement observed gauge: %w", err)
	}

	HLConsensusQCSignaturesCounter, err = meter.Int64Counter(
		"hl_consensus_qc_signatures_total",
		api.WithDescription("Total QC signatures by each validator (validator nodes only)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus QC signatures counter: %w", err)
	}

	HLConsensusQCParticipationGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_qc_participation_percent",
		api.WithDescription("QC signing participation rate per validator as percentage (validator nodes only)"),
		// IMPORTANT: do not set api.WithUnit("%"). The OTel→Prometheus bridge
		// auto-appends "_percent" when "%" is declared as the unit, which would
		// double up with the "_percent" suffix now embedded in the metric name.
		// Keep the percent suffix in the name as the source of truth instead.
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus QC participation gauge: %w", err)
	}

	HLConsensusQCSizeHist, err = meter.Float64Histogram(
		"hl_consensus_qc_size",
		api.WithDescription("Distribution of QC signer counts"),
		api.WithExplicitBucketBoundaries(
			1, 3, 5, 7, 10, 13, 16, 19, 22, 25, 28, 31, 34, 37, 40, 43, 46, 49, 51,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus QC size histogram: %w", err)
	}

	HLConsensusTCBlocksCounter, err = meter.Int64Counter(
		"hl_consensus_tc_blocks_total",
		api.WithDescription("Total blocks proposed with timeout certificates (Note: proposer of TC block is not who caused the timeout)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus TC blocks counter: %w", err)
	}

	HLConsensusTCParticipationCounter, err = meter.Int64Counter(
		"hl_consensus_tc_participation_total",
		api.WithDescription("Total timeout votes sent by each validator (validator nodes only)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus TC participation counter: %w", err)
	}

	HLConsensusTCSizeHist, err = meter.Float64Histogram(
		"hl_consensus_tc_size",
		api.WithDescription("Distribution of TC timeout vote counts"),
		api.WithExplicitBucketBoundaries(
			1, 3, 5, 7, 10, 13, 16, 19, 22, 25, 28, 31, 34, 37, 40, 43, 46, 49, 51,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus TC size histogram: %w", err)
	}

	// efficiency
	HLConsensusRoundsPerBlockGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_rounds_per_block",
		api.WithDescription("Rounds consumed by the most recent block (single sample, ~1 on a healthy chain; not an average)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus rounds per block gauge: %w", err)
	}

	HLConsensusQCRoundLagGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_qc_round_lag",
		api.WithDescription("Round lag (block round - QC round) of the most recent block; 1 on a healthy chain (not an average)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus QC round lag gauge: %w", err)
	}

	// health
	HLConsensusMonitorLastProcessedGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_monitor_last_processed",
		api.WithDescription("Unix timestamp of last processed line by monitor type"),
		api.WithUnit("timestamp"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus monitor last processed gauge: %w", err)
	}

	HLConsensusMonitorLinesCounter, err = meter.Int64Counter(
		"hl_consensus_monitor_lines_processed_total",
		api.WithDescription("Total lines processed by consensus monitor"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus monitor lines counter: %w", err)
	}

	HLConsensusMonitorErrorsCounter, err = meter.Int64Counter(
		"hl_consensus_monitor_errors_total",
		api.WithDescription("Total errors encountered by consensus monitor"),
	)
	if err != nil {
		return fmt.Errorf("failed to create consensus monitor errors counter: %w", err)
	}

	// val latency
	HLConsensusValidatorLatencyGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_latency_seconds",
		api.WithDescription("Current latency for each validator in seconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator latency gauge: %w", err)
	}

	HLConsensusValidatorLatencyRoundGauge, err = meter.Int64ObservableGauge(
		"hl_consensus_validator_latency_round",
		api.WithDescription("Last consensus round number for validator latency measurement"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator latency round gauge: %w", err)
	}

	HLConsensusValidatorLatencyEMAGauge, err = meter.Float64ObservableGauge(
		"hl_consensus_validator_latency_ema_seconds",
		api.WithDescription("Exponential moving average of validator latency in seconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator latency EMA gauge: %w", err)
	}

	// P2P metrics (non-validator peers)
	HLP2PNonValPeerConnectionsGauge, err = meter.Float64ObservableGauge(
		"hl_p2p_non_val_peer_connections",
		api.WithDescription("Deprecated compatibility gauge: explicit child identities in the latest fresh child_peers status snapshot by verification status; despite the name, values are peers, not connections"),
	)
	if err != nil {
		return fmt.Errorf("failed to create P2P non-validator peer connections gauge: %w", err)
	}

	HLP2PNonValPeersTotalGauge, err = meter.Float64ObservableGauge(
		"hl_p2p_non_val_peers_total",
		api.WithDescription("Deprecated compatibility gauge: explicit child identities in the latest fresh child_peers status snapshot; not all connected peers"),
	)
	if err != nil {
		return fmt.Errorf("failed to create P2P non-validator peers total gauge: %w", err)
	}

	return nil
}
