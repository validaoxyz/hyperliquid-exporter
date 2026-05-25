package monitors

import (
	"os"
	"path/filepath"
	"testing"
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
	good := `{"time":"2026-05-25T09:57:48","total_n":1,"mean":1.0,"med":1,"p90":1,"p95":1,"max":1,"std_dev":0,"work_frac":0.1}`
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
