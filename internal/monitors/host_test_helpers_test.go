package monitors

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func hostMetricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatal(err)
	}
	switch {
	case out.Gauge != nil:
		return out.Gauge.GetValue()
	case out.Counter != nil:
		return out.Counter.GetValue()
	default:
		t.Fatalf("unsupported metric type: %T", &out)
		return 0
	}
}
