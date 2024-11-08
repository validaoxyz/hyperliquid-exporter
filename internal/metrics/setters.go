package metrics

import (
	"context"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

func IncrementProposerCounter(proposer string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("validator", proposer),
	}
	HLProposerCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func SetBlockHeight(height int64) {
	logger.Debug("Setting block height to: %d", height)
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLBlockHeightGauge] = height
}

func RecordApplyDuration(duration float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	ctx := context.Background()
	commonLabels := getCommonLabels()

	if HLApplyDurationHistogram != nil {
		HLApplyDurationHistogram.Record(ctx, duration, api.WithAttributes(commonLabels...))
	}

	currentValues[HLApplyDurationGauge] = duration
}

func SetValidatorStake(address, signer, moniker string, stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if _, exists := labeledValues[HLValidatorStakeGauge]; !exists {
		labeledValues[HLValidatorStakeGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLValidatorStakeGauge][address] = labeledValue{
		value: stake,
		labels: []attribute.KeyValue{
			attribute.String("validator", address),
			attribute.String("signer", signer),
			attribute.String("moniker", moniker),
		},
	}
}

func SetValidatorJailedStatus(validator, signer, name string, status float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	labels := append([]attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("signer", signer),
		attribute.String("name", name),
	}, getCommonLabels()...)

	if _, exists := labeledValues[HLValidatorJailedStatus]; !exists {
		labeledValues[HLValidatorJailedStatus] = make(map[string]labeledValue)
	}

	labeledValues[HLValidatorJailedStatus][validator] = labeledValue{
		value:  status,
		labels: labels,
	}
}

func SetTotalStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLTotalStakeGauge] = stake
}

func SetJailedStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLJailedStakeGauge] = stake
}

func SetNotJailedStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLNotJailedStakeGauge] = stake
}

func SetValidatorCount(count int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLValidatorCountGauge] = count
}

func SetSoftwareVersion(version string, commitHash string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if _, exists := labeledValues[HLSoftwareVersionInfo]; !exists {
		labeledValues[HLSoftwareVersionInfo] = make(map[string]labeledValue)
	}
	labeledValues[HLSoftwareVersionInfo]["current"] = labeledValue{
		value: 1,
		labels: []attribute.KeyValue{
			attribute.String("version", version),
			attribute.String("commit_hash", commitHash),
		},
	}
}

func SetSoftwareUpToDate(upToDate bool) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if upToDate {
		currentValues[HLSoftwareUpToDate] = int64(1)
	} else {
		currentValues[HLSoftwareUpToDate] = int64(0)
	}
}

func RecordBlockTime(duration float64) {
	ctx := context.Background()
	commonLabels := getCommonLabels()

	if HLBlockTimeHistogram != nil {
		HLBlockTimeHistogram.Record(ctx, duration, api.WithAttributes(commonLabels...))
	}
}

func SetLatestBlockTime(timestamp int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLLatestBlockTimeGauge] = timestamp
}

func SetEVMBlockHeight(height int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	logger.Debug("Setting EVM block height to: %d", height)
	currentValues[HLEVMBlockHeightGauge] = height
}

// // TODO SetNodeInfo sets the node information metric with constant labels
// func SetNodeInfo() {
// 	logger.Debug("Setting node info metric")
// 	metricsMutex.Lock()
// 	defer metricsMutex.Unlock()

// 	// Set to 1 as this is a constant metric used primarily for its labels
// 	currentValues[HLNodeInfo] = int64(1)
// }

func IncrementEVMTransactionsCounter() {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// Increment the counter by 1
	HLEVMTransactionsCounter.Add(context.Background(), 1)
}

func SetIsValidator(isValidator bool) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	nodeIdentity.IsValidator = isValidator
}

func SetValidatorAddress(address string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	nodeIdentity.ValidatorAddress = address
}

func SetActiveStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLActiveStakeGauge] = stake
}

func SetInactiveStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLInactiveStakeGauge] = stake
}

func SetValidatorActiveStatus(validator, signer, name string, status float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	labels := append([]attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("signer", signer),
		attribute.String("name", name),
	}, getCommonLabels()...)

	if _, exists := labeledValues[HLValidatorActiveStatus]; !exists {
		labeledValues[HLValidatorActiveStatus] = make(map[string]labeledValue)
	}

	labeledValues[HLValidatorActiveStatus][validator] = labeledValue{
		value:  status,
		labels: labels,
	}
}
