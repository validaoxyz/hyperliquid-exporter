package monitors

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func writeAccumFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAccumulatorValidAppendFreshnessAndNeutralDeltas(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "20260808", "1")
	writeAccumFile(t, file,
		`{"time":"2026-08-08T01:00:00","n":1,"delta":-2}`+"\n"+
			`{"time":"2026-08-08T01:00:30","n":null,"delta":4}`+"\n")
	state := &accumulatorBucketState{}
	result, err := drainAccumulatorBucketResult(root, state)
	if err != nil {
		t.Fatal(err)
	}
	if result.validRecords != 1 || result.sum != 0 || !result.invalid || result.latestTime.IsZero() {
		t.Fatalf("neutral/invalid drain = %+v", result)
	}
	result, err = drainAccumulatorBucketResult(root, state)
	if err != nil || result.validRecords != 0 || result.invalid || result.sum != 0 {
		t.Fatalf("EOF re-read refreshed result: %+v, %v", result, err)
	}
	appendAccumFile(t, file, `{"time":"2026-08-08T01:01:00","n":2,"delta":3}`+"\n")
	result, err = drainAccumulatorBucketResult(root, state)
	if err != nil || result.validRecords != 1 || result.sum != 3 {
		t.Fatalf("valid recovery = %+v, %v", result, err)
	}
}

func TestAccumulatorLateDiscoveryBaselinesAtEOF(t *testing.T) {
	nodeHome := t.TempDir()
	root := filepath.Join(nodeHome, "data", "accumulator_buckets", "consensus")
	state := newAccumulatorMonitorState()
	before := hostMetricValue(t, metrics.HLConsensusCommittedBlocks)
	if err := tickAccumulator(root, state); err != nil || state.initialized {
		t.Fatalf("absent tick = initialized %v, err %v", state.initialized, err)
	}
	file := filepath.Join(root, "CommittedBlocks", "hourly", "20260808", "1")
	writeAccumFile(t, file, `{"time":"2026-08-08T01:00:00","n":5,"delta":5}`+"\n")
	if err := tickAccumulator(root, state); err != nil || !state.initialized {
		t.Fatalf("late discovery = initialized %v, err %v", state.initialized, err)
	}
	if after := hostMetricValue(t, metrics.HLConsensusCommittedBlocks); after != before {
		t.Fatalf("late discovery replayed history: before=%v after=%v", before, after)
	}
	appendAccumFile(t, file, `{"time":"2026-08-08T01:00:30","n":2,"delta":2}`+"\n")
	if err := tickAccumulator(root, state); err != nil {
		t.Fatal(err)
	}
	if after := hostMetricValue(t, metrics.HLConsensusCommittedBlocks); after != before+2 {
		t.Fatalf("new append delta = %v, want %v", after, before+2)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := tickAccumulator(root, state); err != nil || state.initialized {
		t.Fatalf("deletion did not reset discovery state: %v, %v", state.initialized, err)
	}
}

func appendAccumFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func TestDrainAccumulatorBucket(t *testing.T) {
	root := t.TempDir()
	f7 := filepath.Join(root, "20260704", "7")
	writeAccumFile(t, f7,
		`{"time":"2026-07-04T07:00:20.531884195","n":484,"delta":484}`+"\n"+
			`{"time":"2026-07-04T07:00:50.573892987","n":493,"delta":564}`+"\n")

	// untracked bucket starting fresh reads the whole file
	st := &accumulatorBucketState{}
	if got := drainAccumulatorBucket(root, st); got != 1048 {
		t.Fatalf("initial drain = %v, want 1048", got)
	}
	if got := drainAccumulatorBucket(root, st); got != 0 {
		t.Fatalf("re-drain without new data = %v, want 0", got)
	}

	// new complete line plus a torn tail: only the complete line counts
	appendAccumFile(t, f7, `{"time":"2026-07-04T07:01:20.1","n":10,"delta":10}`+"\n"+`{"time":"2026-07-04T07:01:5`)
	if got := drainAccumulatorBucket(root, st); got != 10 {
		t.Fatalf("drain with torn tail = %v, want 10", got)
	}

	// completing the torn line makes it count exactly once
	appendAccumFile(t, f7, `0.2","n":3,"delta":3}`+"\n")
	if got := drainAccumulatorBucket(root, st); got != 3 {
		t.Fatalf("drain after completing torn line = %v, want 3", got)
	}

	// hour rollover: tail of the old file and the new file both count
	appendAccumFile(t, f7, `{"time":"2026-07-04T07:59:50.9","n":5,"delta":5}`+"\n")
	writeAccumFile(t, filepath.Join(root, "20260704", "8"),
		`{"time":"2026-07-04T08:00:20.4","n":7,"delta":7}`+"\n")
	if got := drainAccumulatorBucket(root, st); got != 12 {
		t.Fatalf("drain across rollover = %v, want 12", got)
	}
}

func TestDrainAccumulatorBucketNoReplayFromEOF(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "20260704", "7")
	writeAccumFile(t, f, `{"time":"2026-07-04T07:00:20.1","n":484,"delta":484}`+"\n")

	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	// state initialized at EOF, as StartAccumulatorConsensusMonitor does
	st := &accumulatorBucketState{path: f, offset: info.Size()}
	if got := drainAccumulatorBucket(root, st); got != 0 {
		t.Fatalf("drain from EOF init = %v, want 0 (no replay)", got)
	}
	appendAccumFile(t, f, `{"time":"2026-07-04T07:00:50.2","n":2,"delta":9}`+"\n")
	if got := drainAccumulatorBucket(root, st); got != 9 {
		t.Fatalf("drain after append = %v, want 9", got)
	}
}

func TestAccumulatorRejectsNonObjectAndNonfiniteAggregate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "20260808", "1")
	writeAccumFile(t, path,
		`[1]`+"\n"+
			`null`+"\n"+
			`{"time":"2026-08-08T01:00:00","n":1,"delta":1e308}`+"\n"+
			`{"time":"2026-08-08T01:00:30","n":1,"delta":1e308}`+"\n")
	result, err := drainAccumulatorBucketResult(root, &accumulatorBucketState{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.invalid || !result.overflow || result.sum != 0 || result.validRecords != 2 {
		t.Fatalf("invalid/overflow result = %+v", result)
	}
	large := float64(1e308)
	if next := large + large; !math.IsInf(next, 0) {
		t.Fatal("overflow fixture does not overflow float64")
	}
}

func TestAccumulatorRolloverAdvancesAfterOldFileDisappears(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "20260808", "1")
	newPath := filepath.Join(root, "20260808", "2")
	writeAccumFile(t, oldPath, `{"time":"2026-08-08T01:00:00","n":1,"delta":1}`+"\n")
	state := &accumulatorBucketState{}
	if result, err := drainAccumulatorBucketResult(root, state); err != nil || result.sum != 1 {
		t.Fatalf("initial drain = %+v, %v", result, err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	writeAccumFile(t, newPath, `{"time":"2026-08-08T02:00:00","n":2,"delta":2}`+"\n")
	result, err := drainAccumulatorBucketResult(root, state)
	if err == nil || result.sum != 2 || state.path != newPath {
		t.Fatalf("gap rollover = result:%+v path:%q err:%v", result, state.path, err)
	}
	appendAccumFile(t, newPath, `{"time":"2026-08-08T02:00:30","n":3,"delta":3}`+"\n")
	result, err = drainAccumulatorBucketResult(root, state)
	if err != nil || result.sum != 3 || result.validRecords != 1 {
		t.Fatalf("post-gap append = %+v, %v", result, err)
	}
}
