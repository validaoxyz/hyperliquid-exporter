package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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
