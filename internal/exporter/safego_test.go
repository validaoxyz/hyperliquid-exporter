package exporter

import (
	"context"
	"testing"
	"time"
)

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
