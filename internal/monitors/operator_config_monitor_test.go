package monitors

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoads); got != float64(len(operatorConfigFiles)+2) {
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
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigFailedLoads); got != 0 {
		t.Fatalf("reconciled aggregate failed loads = %v, want 0", got)
	}
}
