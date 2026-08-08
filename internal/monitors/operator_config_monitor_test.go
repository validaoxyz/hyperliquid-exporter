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
			body = []byte(`{"dry_run":true,"latency_ema_jail_threshold":0.75}`)
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

func TestScanJailingConfigRequiresFullTypedPair(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "heartbeat_jailing_config.json")
	tests := []struct {
		name      string
		body      string
		wantStage metrics.SourceFailureStage
	}{
		{name: "valid", body: `{"dry_run":false,"latency_ema_jail_threshold":0}`},
		{name: "valid with unknown field", body: `{"dry_run":true,"latency_ema_jail_threshold":0.75,"future":1}`},
		{name: "missing dry run", body: `{"latency_ema_jail_threshold":0.75}`, wantStage: metrics.SourceFailureSchema},
		{name: "missing threshold", body: `{"dry_run":true}`, wantStage: metrics.SourceFailureSchema},
		{name: "null dry run", body: `{"dry_run":null,"latency_ema_jail_threshold":0.75}`, wantStage: metrics.SourceFailureSchema},
		{name: "null threshold", body: `{"dry_run":true,"latency_ema_jail_threshold":null}`, wantStage: metrics.SourceFailureSchema},
		{name: "wrong dry run type", body: `{"dry_run":1,"latency_ema_jail_threshold":0.75}`, wantStage: metrics.SourceFailureDecode},
		{name: "wrong threshold type", body: `{"dry_run":true,"latency_ema_jail_threshold":"0.75"}`, wantStage: metrics.SourceFailureDecode},
		{name: "null document", body: `null`, wantStage: metrics.SourceFailureSchema},
		{name: "malformed", body: `{"dry_run":`, wantStage: metrics.SourceFailureDecode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got := scanJailingConfig(root)
			if tc.wantStage == "" {
				if got.err != nil || got.absent || got.dryRun == nil || got.latencyThresholdSeconds == nil {
					t.Fatalf("valid pair = %+v", got)
				}
				return
			}
			if got.err == nil || got.failureStage != tc.wantStage {
				t.Fatalf("invalid pair = %+v, want stage %q", got, tc.wantStage)
			}
		})
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := scanJailingConfig(root); !got.absent || got.err != nil {
		t.Fatalf("absent pair = %+v", got)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := scanJailingConfig(root); got.err == nil || got.failureStage != metrics.SourceFailureRead {
		t.Fatalf("unreadable pair = %+v, want read failure", got)
	}
}

func TestOperatorConfigJailingPairIsAtomicAndHealthTruthful(t *testing.T) {
	metrics.RegisterSource(metrics.SourceOperatorConfig, true)
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

	// A present but incomplete document is invalid, not a generation with one
	// new value and one retained sibling. Neither series may be initialized.
	if err := os.WriteFile(path, []byte(`{"dry_run":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tickOperatorConfig(root) {
		t.Fatal("partial jailing config was accepted")
	}
	if len(b03CollectorRows(t, metrics.HLNodeJailingThresholdSeconds)) != 0 || len(b03CollectorRows(t, metrics.HLNodeJailingDryRun)) != 0 {
		t.Fatal("partial first generation published a jailing series")
	}

	if err := os.WriteFile(path, []byte(`{"dry_run":false,"latency_ema_jail_threshold":1.25}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("recreated valid jailing config was rejected")
	}
	metrics.PublishMonitorHealthSnapshot()
	source := string(metrics.SourceOperatorConfig)
	sourceAttemptBefore := b04MetricValue(t, metrics.HLExporterSourceLastAttemptSeconds.WithLabelValues(source))
	sourceValidBefore := b04MetricValue(t, metrics.HLExporterSourceLastValidSeconds.WithLabelValues(source))
	sourcePublicationBefore := b04MetricValue(t, metrics.HLExporterSourceLastPublicationSeconds.WithLabelValues(source))
	monitorAttemptBefore := b04MetricValue(t, metrics.HLExporterMonitorLastAttemptSeconds.WithLabelValues("operator_config"))
	monitorValidBefore := b04MetricValue(t, metrics.HLExporterMonitorLastValidSeconds.WithLabelValues("operator_config"))
	monitorPublicationBefore := b04MetricValue(t, metrics.HLExporterMonitorLastPublicationSeconds.WithLabelValues("operator_config"))
	decodeErrors := metrics.HLExporterSourceErrorsTotal.WithLabelValues(source, string(metrics.SourceFailureDecode))
	decodeErrorsBefore := b04MetricValue(t, decodeErrors)
	monitorErrors := metrics.HLExporterMonitorErrorsTotal.WithLabelValues("operator_config")
	monitorErrorsBefore := b04MetricValue(t, monitorErrors)

	// Prove a failed jailing parse does not partially commit otherwise-valid
	// operator metadata or advance the complete-observation clocks.
	otherPath := filepath.Join(root, "crit_msg_ignore.json")
	if err := os.WriteFile(otherPath, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"dry_run":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tickOperatorConfig(root) {
		t.Fatal("malformed jailing body was reported valid")
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingThresholdSeconds.WithLabelValues()); got != 1.25 {
		t.Fatalf("malformed body changed retained threshold to %v", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingDryRun.WithLabelValues()); got != 0 {
		t.Fatalf("malformed body changed retained dry-run to %v", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigPresent.WithLabelValues("crit_msg_ignore.json")); got != 0 {
		t.Fatalf("failed generation partially published config presence %v", got)
	}
	if b04CollectorHasLabels(metrics.HLNodeOperatorConfigAgeSeconds, map[string]string{"file": "crit_msg_ignore.json"}) {
		t.Fatal("failed generation partially published config age")
	}

	metrics.PublishMonitorHealthSnapshot()
	if got := b04MetricValue(t, metrics.HLExporterSourceReadOK.WithLabelValues(source)); got != 1 {
		t.Fatalf("decode failure read_ok=%v, want 1", got)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceSchemaOK.WithLabelValues(source)); got != 0 {
		t.Fatalf("decode failure schema_ok=%v, want 0", got)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceLastAttemptSeconds.WithLabelValues(source)); got <= sourceAttemptBefore {
		t.Fatalf("source attempt clock=%v, want > %v", got, sourceAttemptBefore)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceLastValidSeconds.WithLabelValues(source)); got != sourceValidBefore {
		t.Fatalf("source valid clock=%v, want retained %v", got, sourceValidBefore)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceLastPublicationSeconds.WithLabelValues(source)); got != sourcePublicationBefore {
		t.Fatalf("source publication clock=%v, want retained %v", got, sourcePublicationBefore)
	}
	if got := b04MetricValue(t, metrics.HLExporterMonitorLastAttemptSeconds.WithLabelValues("operator_config")); got <= monitorAttemptBefore {
		t.Fatalf("monitor attempt clock=%v, want > %v", got, monitorAttemptBefore)
	}
	if got := b04MetricValue(t, metrics.HLExporterMonitorLastValidSeconds.WithLabelValues("operator_config")); got != monitorValidBefore {
		t.Fatalf("monitor valid clock=%v, want retained %v", got, monitorValidBefore)
	}
	if got := b04MetricValue(t, metrics.HLExporterMonitorLastPublicationSeconds.WithLabelValues("operator_config")); got != monitorPublicationBefore {
		t.Fatalf("monitor publication clock=%v, want retained %v", got, monitorPublicationBefore)
	}
	if got := b04MetricValue(t, decodeErrors) - decodeErrorsBefore; got != 1 {
		t.Fatalf("decode error delta=%v, want 1", got)
	}
	if got := b04MetricValue(t, monitorErrors) - monitorErrorsBefore; got != 1 {
		t.Fatalf("monitor error delta=%v, want 1", got)
	}

	if err := os.WriteFile(path, []byte(`{"dry_run":true,"latency_ema_jail_threshold":1.5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickOperatorConfig(root) {
		t.Fatal("valid jailing recovery was rejected")
	}
	metrics.PublishMonitorHealthSnapshot()
	if got := b04MetricValue(t, metrics.HLNodeJailingThresholdSeconds.WithLabelValues()); got != 1.5 {
		t.Fatalf("recovered threshold=%v, want 1.5", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeJailingDryRun.WithLabelValues()); got != 1 {
		t.Fatalf("recovered dry-run=%v, want 1", got)
	}
	if got := b04MetricValue(t, metrics.HLNodeOperatorConfigPresent.WithLabelValues("crit_msg_ignore.json")); got != 1 {
		t.Fatalf("recovery did not publish config presence: %v", got)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceSchemaOK.WithLabelValues(source)); got != 1 {
		t.Fatalf("recovered source schema_ok=%v, want 1", got)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceLastValidSeconds.WithLabelValues(source)); got <= sourceValidBefore {
		t.Fatalf("recovered source valid clock=%v, want > %v", got, sourceValidBefore)
	}
	if got := b04MetricValue(t, metrics.HLExporterSourceLastPublicationSeconds.WithLabelValues(source)); got <= sourcePublicationBefore {
		t.Fatalf("recovered source publication clock=%v, want > %v", got, sourcePublicationBefore)
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
