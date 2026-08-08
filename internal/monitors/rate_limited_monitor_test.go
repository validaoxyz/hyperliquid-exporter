package monitors

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestScanRateLimitedStream_EmptyRecentRetainedAndRollover(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	if snapshot, err := scanRateLimitedStream(root, now); err != nil || snapshot.retained != 0 {
		t.Fatalf("healthy empty root: snapshot=%+v err=%v", snapshot, err)
	}
	oldDate := filepath.Join(root, "20260807")
	newDate := filepath.Join(root, "20260808")
	if err := os.MkdirAll(oldDate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDate, "old"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDate, 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(newDate, "fresh")
	stale := filepath.Join(newDate, "stale")
	empty := filepath.Join(newDate, "empty")
	for path, data := range map[string][]byte{fresh: []byte("x"), stale: []byte("x"), empty: nil} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, now.Add(-119*time.Second), now.Add(-119*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, now.Add(-121*time.Second), now.Add(-121*time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := scanRateLimitedStream(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.retained != 2 || snapshot.recent != 1 || !snapshot.lastNonempty.Equal(now.Add(-119*time.Second)) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestScanRateLimitedStream_RecentSpansDateRolloverAndRejectsFutureMtime(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 0, 0, 30, 0, time.UTC)
	oldDate := filepath.Join(root, "20260807")
	newDate := filepath.Join(root, "20260808")
	if err := os.MkdirAll(oldDate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDate, 0o755); err != nil {
		t.Fatal(err)
	}
	previousDayFresh := filepath.Join(oldDate, "23")
	future := filepath.Join(newDate, "future")
	for _, path := range []string{previousDayFresh, future} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(previousDayFresh, now.Add(-60*time.Second), now.Add(-60*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(future, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := scanRateLimitedStream(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.retained != 1 || snapshot.recent != 1 || !snapshot.lastNonempty.Equal(now.Add(-60*time.Second)) {
		t.Fatalf("rollover snapshot=%+v", snapshot)
	}
}

func TestScanRateLimitedStream_MissingRootIsFailure(t *testing.T) {
	_, err := scanRateLimitedStream(filepath.Join(t.TempDir(), "missing"), time.Now())
	if err == nil {
		t.Fatal("missing root produced a healthy zero")
	}
	if scanErr, ok := err.(*rateLimitedScanError); !ok || scanErr.stage != "root" {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root error does not preserve absence identity: %v", err)
	}
}

func TestRateLimitedMissingAndRacedPathsIncrementExactStage(t *testing.T) {
	stream := "abci_stream"
	source := metrics.SourceRateLimitABCI
	metrics.RegisterSource(source, true)

	for _, tc := range []struct {
		stage string
		err   error
	}{
		{stage: "root", err: &rateLimitedScanError{stage: "root", err: os.ErrNotExist}},
		{stage: "date", err: &rateLimitedScanError{stage: "date", err: os.ErrNotExist}},
		{stage: "fileinfo", err: &rateLimitedScanError{stage: "fileinfo", err: os.ErrNotExist}},
	} {
		counter := metrics.HLNodeRateLimitedReadErrorsTotal.WithLabelValues(stream, tc.stage)
		before := counterValue(t, counter)
		markRateLimitedScanFailure(stream, source, tc.err)
		if after := counterValue(t, counter); after != before+1 {
			t.Fatalf("stage %s counter delta=%v", tc.stage, after-before)
		}
	}
}

func counterValue(t *testing.T, counter interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}
