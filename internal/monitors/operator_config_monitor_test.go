package monitors

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestOperatorConfigKnownFilesPresenceAndRemoval(t *testing.T) {
	if len(operatorConfigFiles) != 8 {
		t.Fatalf("operatorConfigFiles count = %d, want fixed eight-file schema", len(operatorConfigFiles))
	}
	root := t.TempDir()
	mtime := time.Now().Add(-time.Minute)
	for _, file := range operatorConfigFiles {
		body := []byte("not interpreted")
		if file == "heartbeat_jailing_config.json" {
			body = []byte("{}")
		}
		path := filepath.Join(root, file)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", file, err)
		}
	}

	// The bug-alert body is deliberately invalid and unreadable; only stat
	// presence/mtime is part of this monitor's contract.
	bugPath := filepath.Join(root, "bug_alert_ack.json")
	if err := os.Chmod(bugPath, 0); err != nil {
		t.Fatalf("chmod bug alert: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bugPath, 0o600) })

	tickOperatorConfig(root)
	for _, file := range operatorConfigFiles {
		if got := b04MetricValue(t, metrics.HLNodeOperatorConfigPresent.WithLabelValues(file)); got != 1 {
			t.Fatalf("presence{%q} = %v, want 1", file, got)
		}
		if !b04CollectorHasLabels(metrics.HLNodeOperatorConfigAgeSeconds, map[string]string{"file": file}) {
			t.Fatalf("age{%q} was not published", file)
		}
	}

	if err := os.Chmod(bugPath, 0o600); err != nil {
		t.Fatalf("restore bug alert mode: %v", err)
	}
	if err := os.Remove(bugPath); err != nil {
		t.Fatalf("remove bug alert: %v", err)
	}
	tickOperatorConfig(root)
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigPresent.WithLabelValues("bug_alert_ack.json")); got != 0 {
		t.Fatalf("removed bug-alert presence = %v, want 0", got)
	}
	if b04CollectorHasLabels(metrics.HLNodeOperatorConfigAgeSeconds, map[string]string{"file": "bug_alert_ack.json"}) {
		t.Fatal("removed bug-alert file left a stale age series")
	}
}

func TestOperatorConfigDiscoversLateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "file_mod_time_tracker")
	if tickOperatorConfig(root) {
		t.Fatal("missing operator-config root was reported valid")
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("late-created operator-config root was not activated")
	}
	for _, file := range operatorConfigFiles {
		if got := b04MetricValue(t, metrics.HLNodeOperatorConfigPresent.WithLabelValues(file)); got != 0 {
			t.Fatalf("late empty root presence{%q} = %v, want 0", file, got)
		}
	}
}

func TestOperatorConfigRootWithdrawalAndValidEmptyRecreation(t *testing.T) {
	metrics.RegisterSource(metrics.SourceOperatorConfig, true)
	parent := t.TempDir()
	root := filepath.Join(parent, "file_mod_time_tracker")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, operatorConfigFiles[0]), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, operatorConfigFiles[0]+"_FAILED_LOAD"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("valid operator-config root was rejected")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if tickOperatorConfig(root) {
		t.Fatal("absent operator-config root was reported valid")
	}
	for name, collector := range map[string]prometheus.Collector{
		"present":     metrics.HLNodeOperatorConfigPresent,
		"age":         metrics.HLNodeOperatorConfigAgeSeconds,
		"failed_load": metrics.HLNodeOperatorConfigFailedLoad,
		"failed_all":  metrics.HLNodeOperatorConfigFailedLoads,
	} {
		if rows := b03CollectorRows(t, collector); len(rows) != 0 {
			t.Fatalf("absent root retained %s rows: %d", name, len(rows))
		}
	}

	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("valid empty operator-config root was rejected")
	}
	if len(b03CollectorRows(t, metrics.HLNodeOperatorConfigPresent)) != len(operatorConfigFiles) {
		t.Fatal("empty root did not publish the fixed presence census")
	}
	if len(b03CollectorRows(t, metrics.HLNodeOperatorConfigFailedLoad)) != len(operatorConfigFiles)+1 {
		t.Fatal("empty root did not publish the fixed failed-load census")
	}
	if rows := b03CollectorRows(t, metrics.HLNodeOperatorConfigFailedLoads); len(rows) != 1 {
		t.Fatalf("empty root failed-load aggregate rows=%d, want 1", len(rows))
	}
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoads.WithLabelValues()); got != 0 {
		t.Fatalf("empty root failed-load aggregate=%v, want 0", got)
	}
}

func TestOperatorConfigJailingValuesWithdrawOnlyOnConfirmedAbsence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "heartbeat_jailing_config.json")
	if err := os.WriteFile(path, []byte(`{"dry_run":true,"latency_ema_jail_threshold":0.75}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("valid jailing config was rejected")
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingThresholdSeconds.WithLabelValues()); got != 0.75 {
		t.Fatalf("threshold=%v, want 0.75", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingDryRun.WithLabelValues()); got != 1 {
		t.Fatalf("dry-run=%v, want 1", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("valid root with absent optional jailing config was rejected")
	}
	if len(b03CollectorRows(t, metrics.HLNodeJailingThresholdSeconds)) != 0 || len(b03CollectorRows(t, metrics.HLNodeJailingDryRun)) != 0 {
		t.Fatal("confirmed jailing-config removal retained current values")
	}

	if err := os.WriteFile(path, []byte(`{"dry_run":false,"latency_ema_jail_threshold":1.25}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("recreated valid jailing config was rejected")
	}
	if err := os.WriteFile(path, []byte(`{"dry_run":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("malformed optional jailing body invalidated the complete presence snapshot")
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingThresholdSeconds.WithLabelValues()); got != 1.25 {
		t.Fatalf("malformed body changed retained threshold to %v", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingDryRun.WithLabelValues()); got != 0 {
		t.Fatalf("malformed body changed retained dry-run to %v", got)
	}
}

func TestOperatorConfigFailedLoadIdentityIsBoundedAndReconciled(t *testing.T) {
	root := t.TempDir()
	for _, file := range operatorConfigFiles {
		if err := os.WriteFile(filepath.Join(root, file+"_FAILED_LOAD"), nil, 0o600); err != nil {
			t.Fatalf("write failed-load sidecar for %s: %v", file, err)
		}
	}
	for _, file := range []string{"arbitrary-secret-name_FAILED_LOAD", "another-new-file_FAILED_LOAD"} {
		if err := os.WriteFile(filepath.Join(root, file), nil, 0o600); err != nil {
			t.Fatalf("write unknown failed-load sidecar: %v", err)
		}
	}

	tickOperatorConfig(root)
	for _, file := range operatorConfigFiles {
		if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoad.WithLabelValues(file)); got != 1 {
			t.Fatalf("failed-load{%q} = %v, want 1", file, got)
		}
	}
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoad.WithLabelValues("unknown")); got != 2 {
		t.Fatalf("failed-load{unknown} = %v, want 2", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoads.WithLabelValues()); got != float64(len(operatorConfigFiles)+2) {
		t.Fatalf("aggregate failed loads = %v, want %d", got, len(operatorConfigFiles)+2)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			continue
		}
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
			t.Fatalf("remove %s: %v", entry.Name(), err)
		}
	}
	tickOperatorConfig(root)
	for _, file := range append(append([]string(nil), operatorConfigFiles...), "unknown") {
		if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoad.WithLabelValues(file)); got != 0 {
			t.Fatalf("reconciled failed-load{%q} = %v, want 0", file, got)
		}
	}
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoads.WithLabelValues()); got != 0 {
		t.Fatalf("reconciled aggregate failed loads = %v, want 0", got)
	}
}
