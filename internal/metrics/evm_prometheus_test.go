package metrics

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	api "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestEVMMetricDescriptorsUseOnlyIntendedLabels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		collector prometheus.Collector
		labels    string
	}{
		{name: "shape", collector: HLEVMTxShapeTotal, labels: "variableLabels: {shape}"},
		{name: "recipient diagnostic", collector: HLEVMRecipientTxTotal, labels: "variableLabels: {address}"},
		{name: "receipt", collector: HLEVMReceiptsTotal, labels: "variableLabels: {outcome}"},
		{name: "mismatch counter", collector: HLEVMTxReceiptCountMismatchesTotal, labels: "variableLabels: {}"},
		{name: "mismatch height", collector: HLEVMTxReceiptLastMismatchHeight, labels: "variableLabels: {}"},
		{name: "system items", collector: HLEVMSystemTransactionItemsTotal, labels: "variableLabels: {}"},
		{name: "precompile", collector: HLEVMReadPrecompileCallsTotal, labels: "variableLabels: {outcome}"},
		{name: "parse errors", collector: HLEVMParseErrorsTotal, labels: "variableLabels: {stage,reason}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			descCh := make(chan *prometheus.Desc, 1)
			tc.collector.Describe(descCh)
			desc := <-descCh
			if got := desc.String(); !strings.Contains(got, tc.labels) {
				t.Fatalf("descriptor %s does not contain %q", got, tc.labels)
			}
		})
	}
	for _, collector := range []prometheus.Collector{
		HLEVMReceiptsTotal,
		HLEVMTxReceiptCountMismatchesTotal,
		HLEVMSystemTransactionItemsTotal,
		HLEVMReadPrecompileCallsTotal,
	} {
		descCh := make(chan *prometheus.Desc, 1)
		collector.Describe(descCh)
		desc := (<-descCh).String()
		for _, forbidden := range []string{"tx_hash", "target", "address", "error="} {
			if strings.Contains(desc, forbidden) {
				t.Fatalf("forbidden label semantics %q in %s", forbidden, desc)
			}
		}
	}
}

func TestWithdrawEVMCurrentSnapshotDeletesOnlyCurrentState(t *testing.T) {
	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	testMeter := provider.Meter("evm-withdraw-test")
	blockHeight, _ := testMeter.Int64ObservableGauge("test_evm_height")
	latestTime, _ := testMeter.Int64ObservableGauge("test_evm_time")
	baseFee, _ := testMeter.Float64ObservableGauge("test_evm_base_fee")
	gasUsed, _ := testMeter.Float64ObservableGauge("test_evm_gas_used")
	gasLimit, _ := testMeter.Float64ObservableGauge("test_evm_gas_limit")
	gasUtil, _ := testMeter.Float64ObservableGauge("test_evm_gas_util")
	priorityFee, _ := testMeter.Float64ObservableGauge("test_evm_priority_fee")

	oldBlockHeight, oldLatestTime := HLEVMBlockHeightGauge, HLEVMLatestBlockTimeGauge
	oldBaseFee, oldGasUsed, oldGasLimit := HLEVMBaseFeeGauge, HLEVMGasUsedGauge, HLEVMGasLimitGauge
	oldGasUtil, oldPriorityFee := HLEVMSGasUtilGauge, HLEVMMaxPriorityFeeGauge
	HLEVMBlockHeightGauge, HLEVMLatestBlockTimeGauge = blockHeight, latestTime
	HLEVMBaseFeeGauge, HLEVMGasUsedGauge, HLEVMGasLimitGauge = baseFee, gasUsed, gasLimit
	HLEVMSGasUtilGauge, HLEVMMaxPriorityFeeGauge = gasUtil, priorityFee
	HLEVMGasUsedRatioAvailable.Reset()
	t.Cleanup(func() {
		metricsMutex.Lock()
		for _, instrument := range []api.Observable{blockHeight, latestTime, baseFee, gasUsed, gasLimit, gasUtil, priorityFee} {
			delete(currentValues, instrument)
			delete(labeledValues, instrument)
		}
		metricsMutex.Unlock()
		HLEVMBlockHeightGauge, HLEVMLatestBlockTimeGauge = oldBlockHeight, oldLatestTime
		HLEVMBaseFeeGauge, HLEVMGasUsedGauge, HLEVMGasLimitGauge = oldBaseFee, oldGasUsed, oldGasLimit
		HLEVMSGasUtilGauge, HLEVMMaxPriorityFeeGauge = oldGasUtil, oldPriorityFee
		HLEVMGasUsedRatioAvailable.Reset()
		HLEVMTxReceiptLastMismatchHeight.Set(0)
	})

	ratio := 0.5
	SetEVMBlockHeight(10)
	SetEVMLatestBlockTime(20)
	SetEVMBaseFeeSnapshot(3)
	SetEVMGasSnapshot(4, 8, &ratio)
	SetEVMPriorityFeeSnapshot(5, false)
	HLEVMTxReceiptLastMismatchHeight.Set(77)
	if rows := metricCollectorRows(HLEVMGasUsedRatioAvailable); rows != 1 {
		t.Fatalf("ratio availability rows before withdrawal = %d, want 1", rows)
	}

	WithdrawEVMCurrentSnapshot()
	metricsMutex.RLock()
	for name, instrument := range map[string]api.Observable{
		"height": blockHeight, "time": latestTime, "base": baseFee, "used": gasUsed,
		"limit": gasLimit, "util": gasUtil, "priority": priorityFee,
	} {
		if _, exists := currentValues[instrument]; exists {
			metricsMutex.RUnlock()
			t.Fatalf("withdrawal retained current %s", name)
		}
	}
	metricsMutex.RUnlock()
	if rows := metricCollectorRows(HLEVMGasUsedRatioAvailable); rows != 0 {
		t.Fatalf("ratio availability rows after withdrawal = %d, want 0", rows)
	}
	var mismatch dto.Metric
	if err := HLEVMTxReceiptLastMismatchHeight.Write(&mismatch); err != nil || mismatch.GetGauge().GetValue() != 77 {
		t.Fatalf("mismatch history changed: value=%v err=%v", mismatch.GetGauge().GetValue(), err)
	}

	SetEVMGasSnapshot(1, 2, &ratio)
	if rows := metricCollectorRows(HLEVMGasUsedRatioAvailable); rows != 1 {
		t.Fatalf("ratio availability rows after recovery = %d, want 1", rows)
	}
}

func metricCollectorRows(collector prometheus.Collector) int {
	ch := make(chan prometheus.Metric, 32)
	collector.Collect(ch)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	return count
}

func TestCanonicalEVMAddress(t *testing.T) {
	t.Parallel()
	if !canonicalEVMAddress("0x0000000000000000000000000000000000000000") {
		t.Fatal("zero address must remain an ordinary canonical recipient")
	}
	for _, invalid := range []string{"", "0x1234", "0X1111111111111111111111111111111111111111", "0xgg11111111111111111111111111111111111111"} {
		if canonicalEVMAddress(invalid) {
			t.Fatalf("accepted invalid address %q", invalid)
		}
	}
}
