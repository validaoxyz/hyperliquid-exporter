package monitors

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestParseCritMsgLine_RealSample(t *testing.T) {
	// Real sample from a live mainnet peer's hl-node crit_msg file.
	line := []byte(`["2026-05-25T09:54:58.011179114",["2026-05-23T08:24:54.982100058",0,113,4]]`)
	bt, bugs, crits, locs, ok := parseCritMsgLine(line)
	if !ok {
		t.Fatalf("parse failed")
	}
	if bt.IsZero() {
		t.Errorf("expected base_time parsed, got zero")
	}
	if bugs != 0 || crits != 113 || locs != 4 {
		t.Errorf("got bugs=%d crits=%d locs=%d, want 0/113/4", bugs, crits, locs)
	}
}

func TestParseCritMsgLine_AllZeros(t *testing.T) {
	line := []byte(`["2026-05-25T09:30:14.690140970",["2026-05-22T08:00:10.495062428",0,0,0]]`)
	bt, b, c, l, ok := parseCritMsgLine(line)
	if !ok {
		t.Fatalf("parse failed")
	}
	if b != 0 || c != 0 || l != 0 {
		t.Errorf("expected all zeros, got %d/%d/%d", b, c, l)
	}
	if bt.IsZero() {
		t.Errorf("expected base_time parsed")
	}
}

func TestParseCritMsgLine_Malformed(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(""),
		[]byte("not json"),
		[]byte("{}"),
		[]byte(`["ts"]`),                      // missing inner
		[]byte(`["ts",[]]`),                   // empty inner
		[]byte(`["ts",["base",1]]`),           // inner too short
		[]byte(`["ts",["base","a","b","c"]]`), // non-integer counts
		[]byte(`["2026-08-08T00:00:00",["2026-08-08T00:00:00",null,0,0]]`),
	} {
		_, _, _, _, ok := parseCritMsgLine(line)
		if ok {
			t.Errorf("expected fail on %q", line)
		}
	}
}

func critDailyFixture(sample, base time.Time, bugs, crits, locations int64) string {
	return fmt.Sprintf(`[%q,[%q,%d,%d,%d]]`, sample.UTC().Format("2006-01-02T15:04:05.999999999"), base.UTC().Format("2006-01-02T15:04:05.999999999"), bugs, crits, locations)
}

func critRichFixture(base time.Time, bugs, crits int64, locations string) string {
	return fmt.Sprintf(`{"start_time":%q,"n_bugs":%d,"n_crits":%d,"code_location_and_stats":%s}`, base.UTC().Format("2006-01-02T15:04:05.999999999"), bugs, crits, locations)
}

func TestCriticalVisorGenerationMatchingRollbackAndEmptyClear(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-time.Hour)
	dailyPath := filepath.Join(root, "hl-visor", now.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(dailyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dailyPath, []byte(critDailyFixture(now, base, 1, 2, 1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newCritGenerationStore()
	dailyState := &critMsgMonitorState{store: store, published: make(map[string]struct{})}
	tickCritMsg(root, dailyState, now.Add(time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeCritsTotal, map[string]string{"source": "hl-visor"}); !ok || value != 2 {
		t.Fatalf("visor daily crits = %v, %v", value, ok)
	}
	if value, ok := b03CollectorValue(t, metrics.HLCriticalMessageSampleTimestamp, map[string]string{"source": "hl-visor"}); !ok || value != float64(now.Unix()) {
		t.Fatalf("visor sample timestamp = %v, %v", value, ok)
	}

	richPath := filepath.Join(t.TempDir(), "hl-visor.json")
	locations := `[[{"fln":"/build/src/critical.rs","line":42},{"n":7,"is_ignored":false,"first_seen":"2026-08-08T00:00:00","last_seen":"2026-08-08T00:01:00","first_msg":"must never become a label"}]]`
	if err := os.WriteFile(richPath, []byte(critRichFixture(base, 1, 2, locations)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(richPath, now, now); err != nil {
		t.Fatal(err)
	}
	richState := &critLocationsState{store: store, active: make(map[[2]string]struct{})}
	if err := tickCritLocationsAt(richPath, richState, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"file": "critical.rs", "line": "42"}
	if value, ok := b03CollectorValue(t, metrics.HLNodeCritLocation, labels); !ok || value != 7 {
		t.Fatalf("matched rich location = %v, %v", value, ok)
	}

	// Truncation and a fully parsed count mismatch both retain the last matched locations.
	if err := os.WriteFile(richPath, []byte(`{"start_time":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tickCritLocationsAt(richPath, richState, now.Add(2*time.Second)); err == nil {
		t.Fatal("truncated rich projection unexpectedly committed")
	}
	if !b03CollectorHasLabels(t, metrics.HLNodeCritLocation, labels) {
		t.Fatal("truncated rich projection cleared last matched location")
	}
	if err := os.WriteFile(richPath, []byte(critRichFixture(base, 1, 99, locations)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(richPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tickCritLocationsAt(richPath, richState, now.Add(3*time.Second)); err == nil {
		t.Fatal("count-mismatched rich projection unexpectedly committed")
	}
	if !b03CollectorHasLabels(t, metrics.HLNodeCritLocation, labels) {
		t.Fatal("count mismatch cleared last matched location")
	}

	// A fresh matched empty generation is the only case that clears locations.
	emptySample := now.Add(4 * time.Second)
	if err := os.WriteFile(dailyPath, []byte(critDailyFixture(emptySample, base, 1, 2, 0)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tickCritMsg(root, dailyState, emptySample.Add(time.Second))
	if err := os.WriteFile(richPath, []byte(critRichFixture(base, 1, 2, `[]`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(richPath, emptySample, emptySample); err != nil {
		t.Fatal(err)
	}
	if err := tickCritLocationsAt(richPath, richState, emptySample.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if b03CollectorHasLabels(t, metrics.HLNodeCritLocation, labels) {
		t.Fatal("matched empty rich generation did not clear old location")
	}
	withdrawCritMessageSource(dailyState, "hl-visor")
}

func TestCriticalStaleHLNodeWithdrawsWithoutFallback(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(root, "hl-node", now.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(critDailyFixture(now.Add(-time.Hour), now.Add(-2*time.Hour), 8, 9, 1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &critMsgMonitorState{store: newCritGenerationStore(), published: map[string]struct{}{"hl-node": {}}}
	publishCritMsg("hl-node", now.Add(-2*time.Hour), 8, 9, 1)
	metrics.HLCriticalMessageSampleTimestamp.WithLabelValues("hl-node").Set(float64(now.Add(-time.Hour).Unix()))
	tickCritMsg(root, state, now)
	if b03CollectorHasLabels(t, metrics.HLNodeCritsTotal, map[string]string{"source": "hl-node"}) {
		t.Fatal("stale hl-node daily projection survived withdrawal")
	}
	if visor, ok := state.store.get("hl-visor"); !ok || visor.available || visor.generation.Source != "" {
		t.Fatalf("missing visor source was silently substituted from hl-node: %+v, %v", visor, ok)
	}
}

func TestReadLastCritMsg_TornTrailingWrite(t *testing.T) {
	// Simulate a file with a complete line followed by a partial (torn)
	// line — the partial JSON would unmarshal-fail, and the reader should
	// walk backwards and return the complete preceding line.
	dir := t.TempDir()
	path := filepath.Join(dir, "20260525")
	contents := `["2026-05-25T09:34:57.989126519",["2026-05-23T08:24:54.982100058",0,113,4]]
["2026-05-25T09:39:57.99` // intentionally torn
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, b, c, l, ok := readLastCritMsg(path)
	if !ok {
		t.Fatalf("expected to recover from torn last line")
	}
	if b != 0 || c != 113 || l != 4 {
		t.Errorf("expected 0/113/4 from preceding line, got %d/%d/%d", b, c, l)
	}
}
