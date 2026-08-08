package metrics

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func TestValidatorAPICacheAgeAdvancesAndClamps(t *testing.T) {
	previous := validatorAPILastSuccessUnix.Load()
	t.Cleanup(func() { validatorAPILastSuccessUnix.Store(previous) })

	lastSuccess := time.Unix(1_800_000_000, 0)
	validatorAPILastSuccessUnix.Store(lastSuccess.Unix())
	publishValidatorAPICacheAgeAt(lastSuccess.Add(10 * time.Second))
	if got := validatorAPIMetricValue(t, HLValidatorAPICacheAgeSeconds); got != 10 {
		t.Fatalf("cache age = %v, want 10", got)
	}
	publishValidatorAPICacheAgeAt(lastSuccess.Add(70 * time.Second))
	if got := validatorAPIMetricValue(t, HLValidatorAPICacheAgeSeconds); got != 70 {
		t.Fatalf("cache age froze at %v, want 70", got)
	}
	publishValidatorAPICacheAgeAt(lastSuccess.Add(-time.Second))
	if got := validatorAPIMetricValue(t, HLValidatorAPICacheAgeSeconds); got != 0 {
		t.Fatalf("future last-success produced negative age %v", got)
	}
}

func validatorAPIMetricValue(t *testing.T, metric interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var row dto.Metric
	if err := metric.Write(&row); err != nil {
		t.Fatal(err)
	}
	return row.GetGauge().GetValue()
}
