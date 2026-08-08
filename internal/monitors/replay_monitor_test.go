package monitors

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestScanReplayUsesEncodedStartAndSeparatesActivity(t *testing.T) {
	root := t.TempDir()
	oldName := "640000000_2026-08-04T00:50:57Z"
	newName := "643440100_2026-08-07T09:11:57Z"
	oldDir := filepath.Join(root, oldName)
	newDir := filepath.Join(root, newName)
	for _, dir := range []string{oldDir, newDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	retouched := time.Date(2026, 8, 8, 17, 17, 0, 0, time.UTC)
	mustChtime(t, oldDir, retouched)
	mustChtime(t, newDir, time.Date(2026, 8, 7, 9, 12, 0, 0, time.UTC))

	got, err := scanReplay(root)
	if err != nil {
		t.Fatal(err)
	}
	wantStart, _ := time.Parse(time.RFC3339Nano, "2026-08-07T09:11:57Z")
	if got.Retained != 2 || got.LatestHeight != 643440100 || !got.LatestStart.Equal(wantStart) || !got.LatestActivity.Equal(retouched) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestScanReplayPruningAndInvalidEntry(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "643440100_2026-08-07T09:11:57Z")
	if err := os.Mkdir(valid, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := scanReplay(root)
	if err != nil || got.Retained != 1 {
		t.Fatalf("valid snapshot = %+v, %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(root, "bad_marker"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = scanReplay(root)
	if !errors.Is(err, errInvalidReplayEntry) || got != (replaySnapshot{}) {
		t.Fatalf("invalid snapshot escaped: %+v, %v", got, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "bad_marker")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(valid); err != nil {
		t.Fatal(err)
	}
	got, err = scanReplay(root)
	if err != nil || got != (replaySnapshot{}) {
		t.Fatalf("empty retained window = %+v, %v", got, err)
	}
}

func TestReplayTickWithdrawsAbsentAndPublishesValidEmpty(t *testing.T) {
	metrics.RegisterSource(metrics.SourceReplay, true)
	parent := t.TempDir()
	root := filepath.Join(parent, "replay")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "643440100_2026-08-07T09:11:57Z"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !tickReplay(root) {
		t.Fatal("valid nonempty replay root was rejected")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if tickReplay(root) {
		t.Fatal("absent replay root was reported valid")
	}
	for name, collector := range map[string]prometheus.Collector{
		"count":    metrics.HLNodeReplayEventsTotal,
		"start":    metrics.HLNodeReplayLastSeconds,
		"height":   metrics.HLNodeReplayLastHeight,
		"activity": metrics.HLNodeReplayLastActivitySeconds,
	} {
		if rows := b03CollectorRows(t, collector); len(rows) != 0 {
			t.Fatalf("absent root retained %s rows: %d", name, len(rows))
		}
	}

	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !tickReplay(root) {
		t.Fatal("valid empty replay root was rejected")
	}
	for name, gauge := range map[string]*prometheus.GaugeVec{
		"count":    metrics.HLNodeReplayEventsTotal,
		"start":    metrics.HLNodeReplayLastSeconds,
		"height":   metrics.HLNodeReplayLastHeight,
		"activity": metrics.HLNodeReplayLastActivitySeconds,
	} {
		if rows := b03CollectorRows(t, gauge); len(rows) != 1 {
			t.Fatalf("empty root %s rows=%d, want 1", name, len(rows))
		}
		if got := hostMetricValue(t, gauge.WithLabelValues()); got != 0 {
			t.Fatalf("empty root %s=%v, want explicit zero", name, got)
		}
	}
}
