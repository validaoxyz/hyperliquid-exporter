package monitors

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSingleInt(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    int64
		ok      bool
	}{
		{"plain", "1007295000", 1007295000, true},
		{"trailing newline", "1009950000\n", 1009950000, true},
		{"leading whitespace", "  42\n", 42, true},
		{"empty", "", 0, false},
		{"non-numeric", "garbage", 0, false},
		{"float — rejected", "1.5", 0, false}, // must be integer
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name)
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := readSingleInt(path)
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestReadSingleInt_Missing(t *testing.T) {
	_, ok := readSingleInt(filepath.Join(t.TempDir(), "no-such-file"))
	if ok {
		t.Fatal("expected ok=false on missing file")
	}
}
