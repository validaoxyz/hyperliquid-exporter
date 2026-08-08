package exporter

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestRunMonitorReturnsAfterLifecycleIsObservable(t *testing.T) {
	const name = "startup_observable_test"
	release := make(chan struct{})
	runMonitor(name, func() { <-release })

	metrics.PublishMonitorHealthSnapshot()
	metric, err := metrics.HLExporterMonitorRunning.GetMetricWithLabelValues(name)
	if err != nil {
		t.Fatal(err)
	}
	var encoded dto.Metric
	if err := metric.Write(&encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.GetGauge().GetValue() != 1 {
		t.Fatalf("running gauge = %v, want 1 when runMonitor returns", encoded.GetGauge().GetValue())
	}

	close(release)
	waitForMonitorWorkers()
}

func TestWaitForMonitorWorkersStopsProducerBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	exited := make(chan struct{})
	runMonitor("shutdown_order_test", func() {
		close(started)
		<-ctx.Done()
		close(exited)
	})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("monitor did not start")
	}
	cancel()
	waitForMonitorWorkers()
	select {
	case <-exited:
	default:
		t.Fatal("wait returned before monitor producer exited")
	}
}
