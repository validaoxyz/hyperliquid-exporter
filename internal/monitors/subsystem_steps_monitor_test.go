package monitors

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestSubsystemStepsCompleteReplacementAndWithdrawal(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	step := "action_delayer_log_status"
	path := filepath.Join(root, "bucket_guard", step, now.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(latencySummaryFixture(now, 1.25)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &subsystemStepsState{published: make(map[stepMetricKey]struct{})}
	if err := tickSubsystemSteps(root, state, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLLatencyBucketGuardWorkFraction, map[string]string{"step": step}); !ok || value != 1.25 {
		t.Fatalf("step work value = %v, %v", value, ok)
	}
	for _, quantile := range []string{"p50", "p90", "p95", "max", "mean", "std_dev"} {
		if !b03CollectorHasLabels(t, metrics.HLLatencyBucketGuard, map[string]string{"step": step, "quantile": quantile}) {
			t.Fatalf("missing quantile %s", quantile)
		}
	}

	// A complete malformed child invalidates the staged scan and retains all labels.
	if err := os.WriteFile(path, []byte(`{"time":"2026-08-08T00:00:00","total_n":null}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tickSubsystemSteps(root, state, now.Add(2*time.Second)); err == nil {
		t.Fatal("malformed child unexpectedly committed")
	}
	if !b03CollectorHasLabels(t, metrics.HLLatencyBucketGuardWorkFraction, map[string]string{"step": step}) {
		t.Fatal("malformed child cleared previous step snapshot")
	}

	if err := os.RemoveAll(filepath.Join(root, "bucket_guard")); err != nil {
		t.Fatal(err)
	}
	if err := tickSubsystemSteps(root, state, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if b03CollectorHasLabels(t, metrics.HLLatencyBucketGuardWorkFraction, map[string]string{"step": step}) {
		t.Fatal("successful empty scan retained step work label")
	}
	for _, quantile := range []string{"p50", "p90", "p95", "max", "mean", "std_dev"} {
		if b03CollectorHasLabels(t, metrics.HLLatencyBucketGuard, map[string]string{"step": step, "quantile": quantile}) {
			t.Fatalf("successful empty scan retained quantile %s", quantile)
		}
	}
}

func TestSubsystemLatencyStaleSummaryWithdraws(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	subsystem := "proposer"
	path := filepath.Join(root, subsystem, now.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(latencySummaryFixture(now.Add(-time.Hour), 3)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &subsystemLatencyState{published: map[string]struct{}{subsystem: {}}}
	publishSubsystemLatency(subsystem, latencySummary{WorkFrac: 9})
	if err := tickSubsystemLatency(root, state, now); err != nil {
		t.Fatal(err)
	}
	if b03CollectorHasLabels(t, metrics.HLNodeSubsystemWorkFrac, map[string]string{"subsystem": subsystem}) {
		t.Fatal("stale summary retained current subsystem series")
	}
}
