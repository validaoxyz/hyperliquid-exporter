package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestValidatorAPICacheClocksAreAbsentBeforeFirstSuccess(t *testing.T) {
	previous := validatorAPILastSuccessUnix.Load()
	t.Cleanup(func() {
		validatorAPILastSuccessUnix.Store(previous)
		HLValidatorAPILastSuccessSeconds.DeleteLabelValues()
		HLValidatorAPICacheAgeSeconds.DeleteLabelValues()
		if previous > 0 {
			HLValidatorAPILastSuccessSeconds.WithLabelValues().Set(float64(previous))
		}
	})

	validatorAPILastSuccessUnix.Store(0)
	HLValidatorAPILastSuccessSeconds.DeleteLabelValues()
	HLValidatorAPICacheAgeSeconds.DeleteLabelValues()
	publishValidatorAPICacheAgeAt(time.Unix(1_800_000_000, 0))

	if got := validatorAPICollectorCount(HLValidatorAPILastSuccessSeconds); got != 0 {
		t.Fatalf("last-success rows before first success = %d, want 0", got)
	}
	if got := validatorAPICollectorCount(HLValidatorAPICacheAgeSeconds); got != 0 {
		t.Fatalf("cache-age rows before first success = %d, want 0", got)
	}
}

func TestValidatorAPICacheAgeAdvancesAndClamps(t *testing.T) {
	previous := validatorAPILastSuccessUnix.Load()
	t.Cleanup(func() {
		validatorAPILastSuccessUnix.Store(previous)
		HLValidatorAPILastSuccessSeconds.DeleteLabelValues()
		HLValidatorAPICacheAgeSeconds.DeleteLabelValues()
		if previous > 0 {
			HLValidatorAPILastSuccessSeconds.WithLabelValues().Set(float64(previous))
		}
	})

	lastSuccess := time.Unix(1_800_000_000, 0)
	validatorAPILastSuccessUnix.Store(lastSuccess.Unix())
	publishValidatorAPICacheAgeAt(lastSuccess.Add(10 * time.Second))
	if got := validatorAPIMetricValue(t, HLValidatorAPICacheAgeSeconds.WithLabelValues()); got != 10 {
		t.Fatalf("cache age = %v, want 10", got)
	}
	publishValidatorAPICacheAgeAt(lastSuccess.Add(70 * time.Second))
	if got := validatorAPIMetricValue(t, HLValidatorAPICacheAgeSeconds.WithLabelValues()); got != 70 {
		t.Fatalf("cache age froze at %v, want 70", got)
	}
	publishValidatorAPICacheAgeAt(lastSuccess.Add(-time.Second))
	if got := validatorAPIMetricValue(t, HLValidatorAPICacheAgeSeconds.WithLabelValues()); got != 0 {
		t.Fatalf("future last-success produced negative age %v", got)
	}
}

func validatorAPICollectorCount(collector prometheus.Collector) int {
	rows := make(chan prometheus.Metric, 16)
	collector.Collect(rows)
	close(rows)
	return len(rows)
}

func validatorAPIMetricValue(t *testing.T, metric interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var row dto.Metric
	if err := metric.Write(&row); err != nil {
		t.Fatal(err)
	}
	return row.GetGauge().GetValue()
}
