package monitors

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func b03CollectorRows(t *testing.T, collector prometheus.Collector) []*dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 1024)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()
	rows := make([]*dto.Metric, 0)
	for metric := range ch {
		var row dto.Metric
		if err := metric.Write(&row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, &row)
	}
	return rows
}

func b03CollectorHasLabels(t *testing.T, collector prometheus.Collector, labels map[string]string) bool {
	t.Helper()
	for _, row := range b03CollectorRows(t, collector) {
		matched := true
		for name, want := range labels {
			found := false
			for _, label := range row.Label {
				if label.GetName() == name && label.GetValue() == want {
					found = true
					break
				}
			}
			if !found {
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

func b03CollectorValue(t *testing.T, collector prometheus.Collector, labels map[string]string) (float64, bool) {
	t.Helper()
	for _, row := range b03CollectorRows(t, collector) {
		matched := true
		for name, want := range labels {
			found := false
			for _, label := range row.Label {
				if label.GetName() == name && label.GetValue() == want {
					found = true
					break
				}
			}
			if !found {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if row.Gauge != nil {
			return row.Gauge.GetValue(), true
		}
		if row.Counter != nil {
			return row.Counter.GetValue(), true
		}
	}
	return 0, false
}

func b03GatherHasFamily(t *testing.T, name string) bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}
