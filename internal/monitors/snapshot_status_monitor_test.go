package monitors

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestScanSnapshotStatusUsesExactlyTwoNewestDateDirectories(t *testing.T) {
	root := t.TempDir()
	for _, date := range []string{"20260524", "20260525", "20260526"} {
		if err := os.Mkdir(filepath.Join(root, date), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteHeight(t, filepath.Join(root, "20260524"), 999_000_000)
	mustWriteHeight(t, filepath.Join(root, "20260525"), 1_000_000_000)
	latestTime := time.Date(2026, 5, 26, 3, 4, 5, 0, time.UTC)
	mustWriteHeightWithMtime(t, filepath.Join(root, "20260526"), 1_000_020_000, latestTime)

	got, err := scanSnapshotStatus(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.DateDirs != 2 || got.Known != 2 || got.LatestHeight != 1_000_020_000 || !got.LatestComplete.Equal(latestTime) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestScanSnapshotStatusRejectsPartialWindow(t *testing.T) {
	root := t.TempDir()
	date := filepath.Join(root, "20260526")
	if err := os.Mkdir(date, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteHeight(t, date, 1_000_020_000)
	if err := os.WriteFile(filepath.Join(date, "garbage"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanSnapshotStatus(root)
	if !errors.Is(err, errInvalidSnapshotStatusEntry) || got != (snapshotStatusSnapshot{}) {
		t.Fatalf("partial snapshot escaped: %+v, %v", got, err)
	}
}

func TestScanSnapshotStatusLateDiscoveryAndEmptyWindow(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "periodic_abci_state_statuses")
	if _, err := scanSnapshotStatus(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root error = %v", err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := scanSnapshotStatus(root)
	if err != nil || got != (snapshotStatusSnapshot{}) {
		t.Fatalf("late empty root = %+v, %v", got, err)
	}
	date := filepath.Join(root, "20260526")
	if err := os.Mkdir(date, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteHeight(t, date, 1_000_020_000)
	got, err = scanSnapshotStatus(root)
	if err != nil || got.Known != 1 || got.LatestHeight != 1_000_020_000 {
		t.Fatalf("late populated root = %+v, %v", got, err)
	}
}

func TestSnapshotStatusTickWithdrawsAbsentAndPublishesValidEmpty(t *testing.T) {
	metrics.RegisterSource(metrics.SourceSnapshotStatus, true)
	parent := t.TempDir()
	root := filepath.Join(parent, "periodic_abci_state_statuses")
	date := filepath.Join(root, "20260526")
	if err := os.MkdirAll(date, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteHeight(t, date, 1_000_020_000)
	if !tickSnapshotStatus(root) {
		t.Fatal("valid nonempty snapshot-status root was rejected")
	}
	if len(b03CollectorRows(t, metrics.HLNodeSnapshotKnown)) != 1 {
		t.Fatal("valid snapshot did not publish current gauges")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if tickSnapshotStatus(root) {
		t.Fatal("absent snapshot-status root was reported valid")
	}
	for name, collector := range map[string]prometheus.Collector{
		"known":         metrics.HLNodeSnapshotKnown,
		"height":        metrics.HLNodeSnapshotLastHeight,
		"age":           metrics.HLNodeSnapshotLastAgeSeconds,
		"lag_available": metrics.HLNodeSnapshotHeightLagAvailable,
		"lag":           metrics.HLNodeSnapshotHeightLagBlocks,
	} {
		if rows := b03CollectorRows(t, collector); len(rows) != 0 {
			t.Fatalf("absent root retained %s rows: %d", name, len(rows))
		}
	}
	metrics.PublishMonitorHealthSnapshot()
	if got := hostMetricValue(t, metrics.HLExporterSourcePresent.WithLabelValues(string(metrics.SourceSnapshotStatus))); got != 0 {
		t.Fatalf("absent source present=%v, want 0", got)
	}

	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !tickSnapshotStatus(root) {
		t.Fatal("valid empty snapshot-status root was rejected")
	}
	for name, gauge := range map[string]*prometheus.GaugeVec{
		"known":         metrics.HLNodeSnapshotKnown,
		"height":        metrics.HLNodeSnapshotLastHeight,
		"age":           metrics.HLNodeSnapshotLastAgeSeconds,
		"lag_available": metrics.HLNodeSnapshotHeightLagAvailable,
	} {
		if rows := b03CollectorRows(t, gauge); len(rows) != 1 {
			t.Fatalf("empty root %s rows=%d, want 1", name, len(rows))
		}
		if got := hostMetricValue(t, gauge.WithLabelValues()); got != 0 {
			t.Fatalf("empty root %s=%v, want explicit zero", name, got)
		}
	}
	if rows := b03CollectorRows(t, metrics.HLNodeSnapshotHeightLagBlocks); len(rows) != 0 {
		t.Fatalf("empty root published unavailable lag rows: %d", len(rows))
	}
	metrics.PublishMonitorHealthSnapshot()
	if got := hostMetricValue(t, metrics.HLExporterSourcePresent.WithLabelValues(string(metrics.SourceSnapshotStatus))); got != 1 {
		t.Fatalf("empty source present=%v, want 1", got)
	}
}

func mustWriteHeight(t *testing.T, dir string, height int64) {
	t.Helper()
	mustWriteHeightWithMtime(t, dir, height, time.Time{})
}

func mustWriteHeightWithMtime(t *testing.T, dir string, height int64, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, strconv.FormatInt(height, 10))
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}
