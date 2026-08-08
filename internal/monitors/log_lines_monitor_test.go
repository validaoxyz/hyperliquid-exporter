package monitors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestCountLinesAndBytes(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		body   string
		lines  int64
		size   int64
		wantOK bool
	}{
		{"empty", "", 0, 0, true},
		{"one line no newline", "hello", 0, 5, true},
		{"one line with newline", "hello\n", 1, 6, true},
		{"three lines", "a\nb\nc\n", 3, 6, true},
		{"three lines trailing partial", "a\nb\nc", 2, 5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name)
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			lines, size, ok := countLinesAndBytes(path)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v", ok, c.wantOK)
			}
			if lines != c.lines {
				t.Errorf("lines=%d want %d", lines, c.lines)
			}
			if size != c.size {
				t.Errorf("size=%d want %d", size, c.size)
			}
		})
	}
}

func TestCountLinesAndBytes_MissingFile(t *testing.T) {
	_, _, ok := countLinesAndBytes(filepath.Join(t.TempDir(), "nope"))
	if ok {
		t.Fatal("expected ok=false on missing file")
	}
}

func TestCountLinesAndBytes_LargeBody(t *testing.T) {
	// 200,000-line file to exercise the 64 KiB chunked read path.
	dir := t.TempDir()
	path := filepath.Join(dir, "big")
	body := strings.Repeat("x\n", 200_000)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, size, ok := countLinesAndBytes(path)
	if !ok {
		t.Fatal("ok=false")
	}
	if lines != 200_000 {
		t.Errorf("lines=%d want 200000", lines)
	}
	if size != 400_000 {
		t.Errorf("size=%d want 400000", size)
	}
}

func TestTickLogLinesDiscoversLateRootAndRetainsOnRemoval(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "log")
	if tickLogLines(root) {
		t.Fatal("missing log root was reported as a valid observation")
	}
	if err := os.MkdirAll(filepath.Join(root, "infra", "error"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "infra", "error", "20260808")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !tickLogLines(root) {
		t.Fatal("late-created log root was not activated")
	}
	labels := map[string]string{"stream": "infra", "level": "error"}
	if got, ok := b03CollectorValue(t, metrics.HLNodeLogLinesTotal, labels); !ok || got != 2 {
		t.Fatalf("late log line count = %v, %v; want 2, true", got, ok)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if tickLogLines(root) {
		t.Fatal("removed log root advanced a valid observation")
	}
	if got, ok := b03CollectorValue(t, metrics.HLNodeLogLinesTotal, labels); !ok || got != 2 {
		t.Fatalf("transient removal changed last complete count = %v, %v", got, ok)
	}
}
