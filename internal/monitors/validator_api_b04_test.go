package monitors

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	hyperliquidapi "github.com/validaoxyz/hyperliquid-exporter/internal/hyperliquid-api"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestParseSummaryStatsBoundsPeriodsAndCountsDrops(t *testing.T) {
	before := b04MetricValue(t, metrics.HLValidatorAPIUnknownPeriodsTotal)
	raw := make([][]json.RawMessage, 0, 101)
	raw = append(raw, b04StatPair("day", "0.99", "0.12"))
	for i := 0; i < 100; i++ {
		raw = append(raw, b04StatPair(fmt.Sprintf("unknown-%d", i), "0.5", "0.1"))
	}

	got := parseSummaryStats(raw)
	if len(got) != 1 || got[0].period != "day" {
		t.Fatalf("parseSummaryStats() = %+v, want only day", got)
	}
	after := b04MetricValue(t, metrics.HLValidatorAPIUnknownPeriodsTotal)
	if after-before != 100 {
		t.Fatalf("unknown-period counter delta = %v, want 100", after-before)
	}
}

func TestValidatorOptionalFieldsReconcileWhenMissingOrInvalid(t *testing.T) {
	const (
		validator = "0xb040000000000000000000000000000000000001"
		signer    = "0xb040000000000000000000000000000000000002"
		name      = "b04-validator"
	)
	reconcileValidatorExtras(nil)
	t.Cleanup(func() { reconcileValidatorExtras(nil) })

	reconcileValidatorExtras([]hyperliquidapi.ValidatorSummary{{
		Validator:       validator,
		Signer:          signer,
		Name:            name,
		NRecentBlocks:   7,
		Commission:      "0.05",
		IsJailed:        true,
		UnjailableAfter: 1_800_000_000_000,
		Stats: [][]json.RawMessage{
			b04StatPair("day", "0.99", "0.12"),
			b04StatPair("week", "0.98", "0.11"),
		},
	}})

	baseLabels := map[string]string{"validator": validator, "signer": signer, "name": name}
	if !b04CollectorHasLabels(metrics.HLConsensusValidatorCommissionRate, baseLabels) {
		t.Fatal("commission series was not published")
	}
	if !b04CollectorHasLabels(metrics.HLConsensusValidatorUnjailableAfter, baseLabels) {
		t.Fatal("unjailable series was not published")
	}
	if !b04CollectorHasLabels(metrics.HLConsensusValidatorUptimeFraction, withB04Period(baseLabels, "week")) {
		t.Fatal("weekly uptime series was not published")
	}

	reconcileValidatorExtras([]hyperliquidapi.ValidatorSummary{{
		Validator:     validator,
		Signer:        signer,
		Name:          name,
		NRecentBlocks: 8,
		Commission:    "invalid",
		Stats: [][]json.RawMessage{
			b04StatPair("day", "NaN", "+Inf"),
		},
	}})

	if b04CollectorHasLabels(metrics.HLConsensusValidatorCommissionRate, baseLabels) {
		t.Fatal("invalid commission left a stale series")
	}
	if b04CollectorHasLabels(metrics.HLConsensusValidatorUnjailableAfter, baseLabels) {
		t.Fatal("absent unjailable field left a stale series")
	}
	for _, period := range []string{"day", "week"} {
		if b04CollectorHasLabels(metrics.HLConsensusValidatorUptimeFraction, withB04Period(baseLabels, period)) {
			t.Fatalf("%s uptime left a stale series", period)
		}
		if b04CollectorHasLabels(metrics.HLConsensusValidatorPredictedApr, withB04Period(baseLabels, period)) {
			t.Fatalf("%s APR left a stale series", period)
		}
	}
}

func TestValidatorOptionalInvalidNumbersDoNotRejectRequiredSnapshot(t *testing.T) {
	_, err := validateValidatorSummaries([]hyperliquidapi.ValidatorSummary{{
		Validator:  "0xb040000000000000000000000000000000000003",
		Signer:     "0xb040000000000000000000000000000000000004",
		Commission: "not-a-number",
		Stats: [][]json.RawMessage{
			b04StatPair("month", "NaN", "not-a-number"),
		},
	}})
	if err != nil {
		t.Fatalf("invalid optional fields rejected complete required snapshot: %v", err)
	}
}

func TestValidateValidatorSummariesRejectsNullGenerationButAllowsExplicitEmpty(t *testing.T) {
	if _, err := validateValidatorSummaries(nil); err == nil {
		t.Fatal("nil validator summary generation was accepted")
	}
	if snapshots, err := validateValidatorSummaries(make([]hyperliquidapi.ValidatorSummary, 0)); err != nil || snapshots == nil {
		t.Fatalf("explicit empty validator summary generation rejected: snapshots=%#v err=%v", snapshots, err)
	}
}

func TestParseSummaryStatsStrictRejectsNullBodyButAllowsNullOptionalScalars(t *testing.T) {
	period := json.RawMessage(`"day"`)
	if _, err := parseSummaryStatsStrict([][]json.RawMessage{{period, json.RawMessage(`null`)}}); err == nil {
		t.Fatal("null stats body was accepted")
	}

	stats, err := parseSummaryStatsStrict([][]json.RawMessage{{
		period,
		json.RawMessage(`{"uptimeFraction":null,"predictedApr":null}`),
	}})
	if err != nil {
		t.Fatalf("null optional stats scalars rejected: %v", err)
	}
	if len(stats) != 1 || stats[0].hasUptime || stats[0].hasApr {
		t.Fatalf("null optional stats scalars published values: %+v", stats)
	}
}

func b04StatPair(period, uptime, apr string) []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(fmt.Sprintf("%q", period)),
		json.RawMessage(fmt.Sprintf("{\"uptimeFraction\":%q,\"predictedApr\":%q}", uptime, apr)),
	}
}

func b04MetricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var row dto.Metric
	if err := metric.Write(&row); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	switch {
	case row.Gauge != nil:
		return row.GetGauge().GetValue()
	case row.Counter != nil:
		return row.GetCounter().GetValue()
	default:
		t.Fatalf("unsupported metric type: %T", metric)
		return 0
	}
}

func b04CollectorHasLabels(collector prometheus.Collector, want map[string]string) bool {
	ch := make(chan prometheus.Metric, 4096)
	collector.Collect(ch)
	close(ch)
	for metric := range ch {
		var row dto.Metric
		if metric.Write(&row) != nil || len(row.Label) != len(want) {
			continue
		}
		matched := true
		for _, label := range row.Label {
			if want[label.GetName()] != label.GetValue() {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func withB04Period(base map[string]string, period string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for key, value := range base {
		out[key] = value
	}
	out["period"] = period
	return out
}
