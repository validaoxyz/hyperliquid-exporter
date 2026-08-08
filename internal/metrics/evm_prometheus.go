package metrics

import (
	"math"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// EVM stream metrics that were added after the original OTel surface use the
// direct Prometheus registry. All label vocabularies are closed except the
// explicitly opt-in recipient diagnostic, whose caller applies a sticky cap.
var (
	HLEVMTxShapeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_evm_tx_shape_total",
		Help: "Accepted user transactions by closed destination shape: create, message, or unknown.",
	}, []string{"shape"})
	HLEVMRecipientTxTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_evm_recipient_tx_total",
		Help: "Opt-in diagnostic count by canonical recipient address; addresses beyond the configured sticky cap use address=\"other\". This does not assert that the recipient is a contract.",
	}, []string{"address"})
	HLEVMReceiptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_evm_receipts_total",
		Help: "Accepted EVM user receipts by boolean execution outcome; false is failed, not necessarily reverted.",
	}, []string{"outcome"})
	HLEVMTxReceiptCountMismatchesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_evm_tx_receipt_count_mismatches_total",
		Help: "Blocks whose transaction-array and receipt-array lengths differ. This is count-only and does not assert receipt identity or order.",
	})
	HLEVMTxReceiptLastMismatchHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_evm_tx_receipt_last_mismatch_height",
		Help: "Height of the most recent transaction/receipt array-count mismatch; zero means none observed by this exporter process.",
	})
	HLEVMSystemTransactionItemsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_evm_system_transaction_items_total",
		Help: "Opaque items observed in structurally valid system_txs arrays; no item semantics or outcomes are inferred.",
	})
	HLEVMReadPrecompileCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_evm_read_precompile_calls_total",
		Help: "Structurally valid nested read-precompile calls by bounded result shape: ok when a non-null Ok member is present, otherwise other.",
	}, []string{"outcome"})
	HLEVMParseErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_evm_parse_errors_total",
		Help: "Rejected EVM stream records by bounded parser stage and reason.",
	}, []string{"stage", "reason"})
	HLEVMGasUsedRatioAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_evm_gas_used_ratio_available",
		Help: "1 when hl_evm_gas_util is available for the latest accepted block, 0 when its gas limit is zero or invalid.",
	}, []string{})
)

func SetEVMGasSnapshot(gasUsed, gasLimit float64, ratio *float64) {
	if gasUsed < 0 || gasLimit < 0 || math.IsNaN(gasUsed) || math.IsNaN(gasLimit) || math.IsInf(gasUsed, 0) || math.IsInf(gasLimit, 0) {
		return
	}
	metricsMutex.Lock()
	// Removing the old labeled state is the explicit in-process deprecation
	// path for the retired heuristic block-tier dimension.
	delete(labeledValues, HLEVMGasUsedGauge)
	delete(labeledValues, HLEVMGasLimitGauge)
	delete(labeledValues, HLEVMSGasUtilGauge)
	currentValues[HLEVMGasUsedGauge] = gasUsed
	currentValues[HLEVMGasLimitGauge] = gasLimit
	if ratio == nil {
		delete(currentValues, HLEVMSGasUtilGauge)
	} else if *ratio >= 0 && *ratio <= 1 && !math.IsNaN(*ratio) && !math.IsInf(*ratio, 0) {
		currentValues[HLEVMSGasUtilGauge] = *ratio
	}
	metricsMutex.Unlock()
	if ratio == nil {
		HLEVMGasUsedRatioAvailable.WithLabelValues().Set(0)
	} else {
		HLEVMGasUsedRatioAvailable.WithLabelValues().Set(1)
	}
}

// WithdrawEVMCurrentSnapshot removes only the latest-block state owned by the
// EVM stream. The caller owns the outer Prometheus snapshot barrier; process
// counters, distributions, mismatch history, and recipient-cap membership are
// intentionally retained across source unavailability.
func WithdrawEVMCurrentSnapshot() {
	metricsMutex.Lock()
	delete(currentValues, HLEVMBlockHeightGauge)
	delete(currentValues, HLEVMLatestBlockTimeGauge)
	delete(currentValues, HLEVMBaseFeeGauge)
	delete(currentValues, HLEVMGasUsedGauge)
	delete(currentValues, HLEVMGasLimitGauge)
	delete(currentValues, HLEVMSGasUtilGauge)
	delete(currentValues, HLEVMMaxPriorityFeeGauge)
	delete(labeledValues, HLEVMBlockHeightGauge)
	delete(labeledValues, HLEVMLatestBlockTimeGauge)
	delete(labeledValues, HLEVMBaseFeeGauge)
	delete(labeledValues, HLEVMGasUsedGauge)
	delete(labeledValues, HLEVMGasLimitGauge)
	delete(labeledValues, HLEVMSGasUtilGauge)
	delete(labeledValues, HLEVMMaxPriorityFeeGauge)
	metricsMutex.Unlock()
	HLEVMGasUsedRatioAvailable.DeleteLabelValues()
}

// SetEVMBaseFeeSnapshot updates the current gauge for valid zero as well as
// nonzero values and records one distribution sample per accepted block.
func SetEVMBaseFeeSnapshot(feeGwei float64) {
	if feeGwei < 0 || math.IsNaN(feeGwei) || math.IsInf(feeGwei, 0) {
		return
	}
	metricsMutex.Lock()
	delete(labeledValues, HLEVMBaseFeeGauge)
	currentValues[HLEVMBaseFeeGauge] = feeGwei
	metricsMutex.Unlock()
	if HLEVMBaseFeeHistogram != nil {
		HLEVMBaseFeeHistogram.Record(sharedCtx, feeGwei)
	}
}

// SetEVMPriorityFeeSnapshot always refreshes the current gauge. The histogram
// is updated only when at least one transaction/receipt supplied a fee value;
// an empty block therefore resets the gauge without fabricating a sample.
func SetEVMPriorityFeeSnapshot(feeGwei float64, observeDistribution bool) {
	if feeGwei < 0 || math.IsNaN(feeGwei) || math.IsInf(feeGwei, 0) {
		return
	}
	metricsMutex.Lock()
	delete(labeledValues, HLEVMMaxPriorityFeeGauge)
	currentValues[HLEVMMaxPriorityFeeGauge] = feeGwei
	metricsMutex.Unlock()
	if observeDistribution && HLEVMPriorityFeeHistogram != nil {
		HLEVMPriorityFeeHistogram.Record(sharedCtx, feeGwei)
	}
}

func RecordEVMTxCount(count int) {
	if count < 0 || HLEVMTxPerBlockHistogram == nil {
		return
	}
	HLEVMTxPerBlockHistogram.Record(sharedCtx, float64(count))
}

func IncrementEVMBoundedTxType(txType string) {
	if HLEVMTxTypeCounter == nil {
		return
	}
	switch txType {
	case "Eip1559", "Legacy", "Eip2930":
	default:
		txType = "other"
	}
	IncrementEVMTxType(txType)
}

func IncrementEVMTxShape(shape string, count uint64) {
	if count == 0 {
		return
	}
	switch shape {
	case "create", "message":
	default:
		shape = "unknown"
	}
	HLEVMTxShapeTotal.WithLabelValues(shape).Add(float64(count))
}

func IncrementEVMRecipientTx(address string) {
	address = strings.ToLower(address)
	if address != "other" && !canonicalEVMAddress(address) {
		address = "other"
	}
	HLEVMRecipientTxTotal.WithLabelValues(address).Inc()
}

func IncrementEVMReceiptOutcome(outcome string, count uint64) {
	if count == 0 {
		return
	}
	switch outcome {
	case "success", "failed":
	default:
		outcome = "unknown"
	}
	HLEVMReceiptsTotal.WithLabelValues(outcome).Add(float64(count))
}

func MarkEVMTxReceiptCountMismatch(height int64) {
	HLEVMTxReceiptCountMismatchesTotal.Inc()
	HLEVMTxReceiptLastMismatchHeight.Set(float64(height))
}

func AddEVMSystemTransactionItems(count int) {
	if count > 0 {
		HLEVMSystemTransactionItemsTotal.Add(float64(count))
	}
}

func AddEVMReadPrecompileCalls(outcome string, count uint64) {
	if count == 0 {
		return
	}
	if outcome != "ok" {
		outcome = "other"
	}
	HLEVMReadPrecompileCallsTotal.WithLabelValues(outcome).Add(float64(count))
}

func IncrementEVMParseError(stage, reason string) {
	switch stage {
	case "envelope", "timestamp", "payload", "block", "header", "transactions", "receipts", "system_txs", "precompile_calls":
	default:
		stage = "other"
	}
	switch reason {
	case "invalid_json", "missing", "wrong_type", "invalid_value", "out_of_range":
	default:
		reason = "other"
	}
	HLEVMParseErrorsTotal.WithLabelValues(stage, reason).Inc()
}

func canonicalEVMAddress(address string) bool {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return false
	}
	for _, c := range address[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
