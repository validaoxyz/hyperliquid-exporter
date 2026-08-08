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

func TestScanReplicaRunsUsesNameForStartAndMtimeForActivity(t *testing.T) {
	root := t.TempDir()
	oldName := "2026-05-23T08:25:08Z"
	newName := "2026-05-25T11:13:57Z"
	oldDir := filepath.Join(root, oldName)
	newDir := filepath.Join(root, newName)
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Deliberately retouch the older run after the newer one. Start selection
	// must remain name-derived while activity reports the retouch.
	oldActivity := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	newActivity := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	mustChtime(t, oldDir, oldActivity)
	mustChtime(t, newDir, newActivity)

	got, err := scanReplicaRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	wantStart, _ := time.Parse(time.RFC3339Nano, newName)
	if got.Retained != 2 || !got.LatestStart.Equal(wantStart) || !got.LatestActivity.Equal(oldActivity) {
		t.Fatalf("snapshot = %+v", got)
	}

	if err := os.RemoveAll(oldDir); err != nil {
		t.Fatal(err)
	}
	got, err = scanReplicaRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Retained != 1 || !got.LatestStart.Equal(wantStart) {
		t.Fatalf("post-prune snapshot = %+v", got)
	}
}

func TestScanReplicaRunsRejectsInvalidDirectoryWithoutPartialSnapshot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"2026-05-25T11:13:57Z", "not-a-run"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := scanReplicaRuns(root)
	if !errors.Is(err, errInvalidReplicaRunEntry) {
		t.Fatalf("error = %v", err)
	}
	if got != (replicaRunsSnapshot{}) {
		t.Fatalf("partial snapshot escaped: %+v", got)
	}
}

func TestScanReplicaRunsEmptyRootIsValid(t *testing.T) {
	got, err := scanReplicaRuns(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != (replicaRunsSnapshot{}) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestReplicaRunsTickWithdrawsAbsentAndPublishesValidEmpty(t *testing.T) {
	metrics.RegisterSource(metrics.SourceReplicaRuns, true)
	parent := t.TempDir()
	root := filepath.Join(parent, "replica_cmds")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "2026-05-25T11:13:57Z"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !tickReplicaRuns(root) {
		t.Fatal("valid nonempty replica-runs root was rejected")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if tickReplicaRuns(root) {
		t.Fatal("absent replica-runs root was reported valid")
	}
	for name, collector := range map[string]prometheus.Collector{
		"count":    metrics.HLNodeObservedRunsTotal,
		"start":    metrics.HLNodeObservedRunStartSeconds,
		"activity": metrics.HLNodeObservedRunLastActivitySeconds,
	} {
		if rows := b03CollectorRows(t, collector); len(rows) != 0 {
			t.Fatalf("absent root retained %s rows: %d", name, len(rows))
		}
	}

	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !tickReplicaRuns(root) {
		t.Fatal("valid empty replica-runs root was rejected")
	}
	for name, gauge := range map[string]*prometheus.GaugeVec{
		"count":    metrics.HLNodeObservedRunsTotal,
		"start":    metrics.HLNodeObservedRunStartSeconds,
		"activity": metrics.HLNodeObservedRunLastActivitySeconds,
	} {
		if rows := b03CollectorRows(t, gauge); len(rows) != 1 {
			t.Fatalf("empty root %s rows=%d, want 1", name, len(rows))
		}
		if got := hostMetricValue(t, gauge.WithLabelValues()); got != 0 {
			t.Fatalf("empty root %s=%v, want explicit zero", name, got)
		}
	}
}

func mustChtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
