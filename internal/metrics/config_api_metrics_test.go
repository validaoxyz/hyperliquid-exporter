package metrics

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestInitMetricsRejectsInvalidChainBeforeIdentityResolution(t *testing.T) {
	metricsMutex.Lock()
	previous := nodeIdentity
	sentinel := NodeIdentity{Alias: "unchanged", Chain: "sentinel"}
	nodeIdentity = sentinel
	metricsMutex.Unlock()
	t.Cleanup(func() {
		metricsMutex.Lock()
		nodeIdentity = previous
		metricsMutex.Unlock()
	})

	_, err := InitMetrics(context.Background(), MetricsConfig{Chain: "devnet"})
	if err == nil {
		t.Fatal("InitMetrics(devnet) unexpectedly succeeded")
	}
	metricsMutex.RLock()
	got := nodeIdentity
	metricsMutex.RUnlock()
	if got != sentinel {
		t.Fatalf("invalid chain reached identity resolution: got %+v, want unchanged %+v", got, sentinel)
	}
}

func TestSetConfiguredChainIsNormalizedAndImmutable(t *testing.T) {
	if err := SetConfiguredChain(" MainNet "); err != nil {
		t.Fatalf("SetConfiguredChain(mainnet) error = %v", err)
	}
	if err := SetConfiguredChain("MAINNET"); err != nil {
		t.Fatalf("idempotent SetConfiguredChain(mainnet) error = %v", err)
	}
	if err := SetConfiguredChain("testnet"); err == nil {
		t.Fatal("SetConfiguredChain(testnet) changed an immutable process identity")
	}
	if err := SetConfiguredChain("devnet"); err == nil {
		t.Fatal("SetConfiguredChain(devnet) unexpectedly succeeded")
	}

	ch := make(chan prometheus.Metric, 4)
	HLExporterConfigInfo.Collect(ch)
	close(ch)
	var got []*dto.Metric
	for metric := range ch {
		var row dto.Metric
		if err := metric.Write(&row); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		got = append(got, &row)
	}
	if len(got) != 1 {
		t.Fatalf("config-info series count = %d, want 1", len(got))
	}
	if got[0].GetGauge().GetValue() != 1 {
		t.Fatalf("config-info value = %v, want 1", got[0].GetGauge().GetValue())
	}
	if len(got[0].Label) != 1 || got[0].Label[0].GetName() != "chain" || got[0].Label[0].GetValue() != "mainnet" {
		t.Fatalf("config-info labels = %+v, want only chain=mainnet", got[0].Label)
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "hl_exporter_config_info" {
			if !strings.Contains(strings.ToLower(family.GetHelp()), "configured") ||
				!strings.Contains(strings.ToLower(family.GetHelp()), "does not attest") {
				t.Fatalf("HELP does not preserve configured-vs-observed boundary: %q", family.GetHelp())
			}
			return
		}
	}
	t.Fatal("hl_exporter_config_info was not gathered")
}
