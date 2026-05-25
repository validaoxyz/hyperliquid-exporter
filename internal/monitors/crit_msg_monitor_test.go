package monitors

import (
	"os"
	"path/filepath"
	"testing"
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
		[]byte(`["ts"]`),                   // missing inner
		[]byte(`["ts",[]]`),                // empty inner
		[]byte(`["ts",["base",1]]`),        // inner too short
		[]byte(`["ts",["base","a","b","c"]]`), // non-integer counts
	} {
		_, _, _, _, ok := parseCritMsgLine(line)
		if ok {
			t.Errorf("expected fail on %q", line)
		}
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
