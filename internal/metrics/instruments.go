package metrics

import (
	"fmt"

	api "go.opentelemetry.io/otel/metric"
)

// Metric instruments for the Hyperliquid exporter
var (
	// Counters
	HLProposerCounter        api.Int64Counter
	HLEVMTransactionsCounter api.Int64Counter

	// Observable Gauges
	HLBlockHeightGauge      api.Int64ObservableGauge
	HLApplyDurationGauge    api.Float64ObservableGauge
	HLValidatorJailedStatus api.Float64ObservableGauge
	HLValidatorStakeGauge   api.Float64ObservableGauge
	HLTotalStakeGauge       api.Float64ObservableGauge
	HLJailedStakeGauge      api.Float64ObservableGauge
	HLNotJailedStakeGauge   api.Float64ObservableGauge
	HLValidatorCountGauge   api.Int64ObservableGauge
	HLSoftwareVersionInfo   api.Int64ObservableGauge
	HLSoftwareUpToDate      api.Int64ObservableGauge
	HLLatestBlockTimeGauge  api.Int64ObservableGauge
	HLEVMBlockHeightGauge   api.Int64ObservableGauge
	HLActiveStakeGauge      api.Float64ObservableGauge
	HLInactiveStakeGauge    api.Float64ObservableGauge
	HLValidatorActiveStatus api.Float64ObservableGauge
	HLValidatorRTTGauge     api.Float64ObservableGauge

	// Histograms
	HLBlockTimeHistogram     api.Float64Histogram
	HLApplyDurationHistogram api.Float64Histogram

	// TODO
	// // Node Info
	// HLNodeInfo api.Int64ObservableGauge
)

func createInstruments() error {
	var err error

	blockTimeBuckets := []float64{
		10, 20, 30, 40, 50, 60, 70, 80, 90, 100,
		120, 140, 160, 180, 200, 220, 240, 260, 280, 300,
		350, 400, 450, 500, 600, 700, 800,
		900, 1000, 1500, 2000,
	}

	HLBlockTimeHistogram, err = meter.Float64Histogram(
		"hl_block_time_milliseconds",
		api.WithDescription("Distribution of time between blocks in milliseconds"),
		api.WithUnit("ms"),
		api.WithExplicitBucketBoundaries(blockTimeBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create block time histogram: %w", err)
	}

	applyDurationBuckets := []float64{
		0.1, 0.2, 0.5, 1, 2, 3, 5, 7, 10, 15, 20,
		30, 50, 75, 100, 150, 160, 170, 180, 190,
		200, 210, 220, 230, 240, 250,
	}

	HLApplyDurationHistogram, err = meter.Float64Histogram(
		"hl_apply_duration_milliseconds",
		api.WithDescription("Distribution of block apply durations in milliseconds"),
		api.WithUnit("ms"),
		api.WithExplicitBucketBoundaries(applyDurationBuckets...),
	)
	if err != nil {
		return fmt.Errorf("failed to create apply duration histogram: %w", err)
	}

	// TODO
	// HLNodeInfo, err = meter.Int64ObservableGauge(
	// 	"hl_node_info",
	// 	api.WithDescription("Node identification information"),
	// )
	// if err != nil {
	// 	return fmt.Errorf("failed to create node info gauge: %w", err)
	// }

	HLProposerCounter, err = meter.Int64Counter(
		"hl_proposer_count_total",
		api.WithDescription("Total number of blocks proposed by each validator"),
	)
	if err != nil {
		return fmt.Errorf("failed to create proposer counter: %w", err)
	}

	HLBlockHeightGauge, err = meter.Int64ObservableGauge(
		"hl_block_height",
		api.WithDescription("Current block height of the chain"),
	)
	if err != nil {
		return fmt.Errorf("failed to create block height gauge: %w", err)
	}

	HLApplyDurationGauge, err = meter.Float64ObservableGauge(
		"hl_apply_duration",
		api.WithDescription("Duration of block application"),
	)
	if err != nil {
		return fmt.Errorf("failed to create apply duration gauge: %w", err)
	}

	HLValidatorJailedStatus, err = meter.Float64ObservableGauge(
		"hl_validator_jailed_status",
		api.WithDescription("Validator jail status (0=not jailed, 1=jailed)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator jailed status gauge: %w", err)
	}

	HLValidatorStakeGauge, err = meter.Float64ObservableGauge(
		"hl_validator_stake",
		api.WithDescription("Stake amount for each validator"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator stake gauge: %w", err)
	}

	HLTotalStakeGauge, err = meter.Float64ObservableGauge(
		"hl_total_stake",
		api.WithDescription("Total stake in the network"),
	)
	if err != nil {
		return fmt.Errorf("failed to create total stake gauge: %w", err)
	}

	HLJailedStakeGauge, err = meter.Float64ObservableGauge(
		"hl_jailed_stake",
		api.WithDescription("Total jailed stake"),
	)
	if err != nil {
		return fmt.Errorf("failed to create jailed stake gauge: %w", err)
	}

	HLNotJailedStakeGauge, err = meter.Float64ObservableGauge(
		"hl_not_jailed_stake",
		api.WithDescription("Total not jailed stake"),
	)
	if err != nil {
		return fmt.Errorf("failed to create not jailed stake gauge: %w", err)
	}

	HLValidatorCountGauge, err = meter.Int64ObservableGauge(
		"hl_validator_count",
		api.WithDescription("Total number of validators"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator count gauge: %w", err)
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

	HLLatestBlockTimeGauge, err = meter.Int64ObservableGauge(
		"hl_latest_block_time",
		api.WithDescription("Latest block time"),
	)
	if err != nil {
		return fmt.Errorf("failed to create latest block time gauge: %w", err)
	}

	HLEVMBlockHeightGauge, err = meter.Int64ObservableGauge(
		"hl_evm_block_height",
		api.WithDescription("Current block height of the EVM chain"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM block height gauge: %w", err)
	}

	HLEVMTransactionsCounter, err = meter.Int64Counter(
		"hl_evm_transactions_total",
		api.WithDescription("Total number of EVM transactions processed"),
	)
	if err != nil {
		return fmt.Errorf("failed to create EVM transactions counter: %w", err)
	}

	HLActiveStakeGauge, err = meter.Float64ObservableGauge(
		"hl_active_stake",
		api.WithDescription("Total stake of active validators in the Hyperliquid network"),
	)
	if err != nil {
		return fmt.Errorf("failed to create active stake gauge: %w", err)
	}

	HLInactiveStakeGauge, err = meter.Float64ObservableGauge(
		"hl_inactive_stake",
		api.WithDescription("Total stake of inactive validators in the Hyperliquid network"),
	)
	if err != nil {
		return fmt.Errorf("failed to create inactive stake gauge: %w", err)
	}

	HLValidatorActiveStatus, err = meter.Float64ObservableGauge(
		"hl_validator_active_status",
		api.WithDescription("Active status of each validator (1 if active, 0 if not active)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator active status gauge: %w", err)
	}

	HLValidatorRTTGauge, err = meter.Float64ObservableGauge(
		"hl_validator_rtt",
		api.WithDescription("Round-trip time (RTT) to validator nodes in milliseconds"),
	)
	if err != nil {
		return fmt.Errorf("failed to create validator RTT gauge: %w", err)
	}

	return nil
}
