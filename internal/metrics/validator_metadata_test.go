package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestValidatorGaugeMetadataUsesPluralStateNouns(t *testing.T) {
	declared := declaredMetricKinds(t)
	for _, name := range []string{
		"hl_consensus_validators",
		"hl_consensus_api_active_and_unjailed_validators",
		"hl_consensus_status_api_active_and_unjailed_validators",
	} {
		if got := declared[name]; got != "gauge" {
			t.Fatalf("%s kind = %q, want gauge", name, got)
		}
	}
	for _, obsolete := range []string{
		"hl_consensus_validator_count",
		"hl_consensus_api_active_and_unjailed_validator_count",
		"hl_consensus_status_api_active_and_unjailed_count",
	} {
		if _, exists := declared[obsolete]; exists {
			t.Fatalf("obsolete gauge name still declared: %s", obsolete)
		}
	}
}

func TestValidatorLegacyNodeStateMetadataIsExplicitlyDeprecated(t *testing.T) {
	for name, collector := range map[string]prometheus.Collector{
		"scheduled":  HLVisorScheduledFreezeHeight,
		"freeze":     HLVisorFreezeAbciHeight,
		"above":      HLVisorBlocksAboveFreeze,
		"checkpoint": HLEVMDBCheckpointHeight,
		"gap":        HLEVMDBCheckpointLagBlocks,
	} {
		descriptor := validatorDescriptorText(collector)
		if !strings.Contains(descriptor, "Deprecated") {
			t.Fatalf("%s descriptor lacks deprecation contract: %s", name, descriptor)
		}
	}
}

func validatorDescriptorText(collector prometheus.Collector) string {
	descs := make(chan *prometheus.Desc, 8)
	collector.Describe(descs)
	close(descs)
	var out strings.Builder
	for desc := range descs {
		out.WriteString(desc.String())
	}
	return out.String()
}
