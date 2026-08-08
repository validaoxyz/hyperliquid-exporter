package monitors

import (
	"context"
	"errors"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestReportErrorFullChannelDoesNotStallProducer(t *testing.T) {
	errCh := make(chan error, 1)
	errCh <- errors.New("already queued")
	before := errorDropCounterValue(t, "block")
	done := make(chan struct{})
	go func() {
		ReportError(context.Background(), "block", errCh, errors.New("dropped"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full error channel stalled its monitor producer")
	}
	if got := errorDropCounterValue(t, "block"); got != before+1 {
		t.Fatalf("drop counter = %v, want %v", got, before+1)
	}
}

func TestReportErrorDeliveryAndCancellation(t *testing.T) {
	errCh := make(chan error, 1)
	want := errors.New("delivered")
	if !ReportError(context.Background(), "version", errCh, want) {
		t.Fatal("writable channel did not accept error")
	}
	if got := <-errCh; !errors.Is(got, want) {
		t.Fatalf("delivered error = %v, want %v", got, want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := errorDropCounterValue(t, "version")
	if ReportError(ctx, "version", errCh, errors.New("shutdown")) {
		t.Fatal("cancelled report was delivered")
	}
	if got := errorDropCounterValue(t, "version"); got != before {
		t.Fatalf("cancellation changed drop counter from %v to %v", before, got)
	}
}

func TestMonitorErrorDropVocabularyAndLogThrottle(t *testing.T) {
	if got := fixedErrorMonitorName("unbounded-user-input"); got != "other" {
		t.Fatalf("unknown monitor normalized to %q", got)
	}
	monitor := "log_throttle_test"
	monitorErrorDropLogs.Lock()
	delete(monitorErrorDropLogs.last, monitor)
	monitorErrorDropLogs.Unlock()
	t0 := time.Unix(1_800_000_000, 0)
	if !shouldLogMonitorErrorDrop(monitor, t0) {
		t.Fatal("first drop was not loggable")
	}
	if shouldLogMonitorErrorDrop(monitor, t0.Add(59*time.Second)) {
		t.Fatal("drop log was not throttled")
	}
	if !shouldLogMonitorErrorDrop(monitor, t0.Add(time.Minute)) {
		t.Fatal("drop log did not reopen after interval")
	}
}

func errorDropCounterValue(t *testing.T, monitor string) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := metrics.HLExporterMonitorErrorDropsTotal.WithLabelValues(monitor).Write(metric); err != nil {
		t.Fatalf("write drop counter: %v", err)
	}
	return metric.GetCounter().GetValue()
}
