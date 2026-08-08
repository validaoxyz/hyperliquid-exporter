package monitors

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestReadLastSummary_RealSample(t *testing.T) {
	// Real sample from node_fast_begin_block_to_commit/<date> on a live
	// mainnet peer.
	line := `{"time":"2026-05-25T09:57:48.041484537","total_n":522583,"total_mean":0.013836266979599502,"n_buffer":2000,"work_frac":0.20158894558399418,"mean":0.011538491999999997,"med":0.00985,"p90":0.022832,"p95":0.02861,"max":0.053403,"std_dev":0.00799840272010434,"bucket_mean":0.010863827664399103,"bucket_work_frac":0.15968112223684267,"bucket_n":441,"bucket_n_orig":441,"is_subsampled":false}`
	dir := t.TempDir()
	path := filepath.Join(dir, "20260525")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, ok := readLastSummary(path)
	if !ok {
		t.Fatal("expected ok")
	}
	if s.Mean != 0.011538491999999997 || s.P95 != 0.02861 || s.TotalN != 522583 {
		t.Errorf("mismatch: %+v", s)
	}
}

func TestReadLastSummary_TornLastLine(t *testing.T) {
	good := `{"time":"2026-05-25T09:57:48","total_n":1,"total_mean":1,"mean":1.0,"med":1,"p90":1,"p95":1,"max":1,"std_dev":0,"work_frac":0.1}`
	torn := `{"time":"2026-05-25T09:58:18","total_n":2,"mea`
	dir := t.TempDir()
	path := filepath.Join(dir, "20260525")
	if err := os.WriteFile(path, []byte(good+"\n"+torn), 0o644); err != nil {
		t.Fatal(err)
	}
	s, ok := readLastSummary(path)
	if !ok {
		t.Fatal("expected to recover from torn last line")
	}
	if s.TotalN != 1 {
		t.Errorf("expected to read 'good' line, got total_n=%d", s.TotalN)
	}
}

func TestReadLastSummary_OversizedTornRecordIsNotEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "20260525")
	if err := os.WriteFile(path, []byte(`{"time":"`+strings.Repeat("x", 5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLastSummaryComplete(path); !errors.Is(err, errPartialSummary) {
		t.Fatalf("oversized torn record error = %v, want partial", err)
	}
}

func latencySummaryFixture(at time.Time, work float64) string {
	return fmt.Sprintf(`{"time":%q,"total_n":7,"total_mean":0.2,"mean":0.3,"med":0.25,"p90":0.4,"p95":0.5,"max":0.6,"std_dev":0.1,"work_frac":%g}`, at.UTC().Format("2006-01-02T15:04:05.999999999"), work)
}

func TestSubsystemLatencyCompleteReplacementRollbackAndRawWorkValue(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	subsystem := "execution_sender"
	path := filepath.Join(root, subsystem, now.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(latencySummaryFixture(now, 1.75)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &subsystemLatencyState{published: make(map[string]struct{})}
	if err := tickSubsystemLatency(root, state, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLNodeSubsystemWorkFrac, map[string]string{"subsystem": subsystem}); !ok || value != 1.75 {
		t.Fatalf("raw work value = %v, %v; want 1.75", value, ok)
	}
	// A malformed complete update cannot partially replace the last good generation.
	if err := os.WriteFile(path, []byte(`{"time":null,"total_n":8}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tickSubsystemLatency(root, state, now.Add(2*time.Second)); err == nil {
		t.Fatal("malformed complete summary unexpectedly committed")
	}
	if value, ok := b03CollectorValue(t, metrics.HLNodeSubsystemWorkFrac, map[string]string{"subsystem": subsystem}); !ok || value != 1.75 {
		t.Fatalf("malformed rollback value = %v, %v", value, ok)
	}

	if err := os.RemoveAll(filepath.Join(root, subsystem)); err != nil {
		t.Fatal(err)
	}
	if err := tickSubsystemLatency(root, state, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, collector := range []prometheus.Collector{
		metrics.HLNodeSubsystemLatencyMean, metrics.HLNodeSubsystemLatencyMedian,
		metrics.HLNodeSubsystemLatencyP90, metrics.HLNodeSubsystemLatencyP95,
		metrics.HLNodeSubsystemLatencyMax, metrics.HLNodeSubsystemLatencyStdDev,
		metrics.HLNodeSubsystemWorkFrac, metrics.HLNodeSubsystemSamplesTotal,
		metrics.HLNodeSubsystemLatencyLifetimeMean,
	} {
		if b03CollectorHasLabels(t, collector, map[string]string{"subsystem": subsystem}) {
			t.Fatalf("owned subsystem label survived empty replacement")
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(latencySummaryFixture(now.Add(4*time.Second), 2.25)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tickSubsystemLatency(root, state, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLNodeSubsystemWorkFrac, map[string]string{"subsystem": subsystem}); !ok || value != 2.25 {
		t.Fatalf("recreated work value = %v, %v", value, ok)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := tickSubsystemLatency(root, state, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if b03CollectorHasLabels(t, metrics.HLNodeSubsystemWorkFrac, map[string]string{"subsystem": subsystem}) {
		t.Fatal("label survived confirmed source deletion")
	}
}

func TestLatestDateFile(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"20260524", "20260525", "20260523"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := latestDateFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "20260525" {
		t.Errorf("got %q, want suffix 20260525", got)
	}
}
