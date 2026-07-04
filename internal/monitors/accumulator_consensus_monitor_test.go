package monitors

import (
	"os"
	"path/filepath"
	"testing"
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
