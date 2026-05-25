package monitors

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLatestHourlyFile_NumericHourSort(t *testing.T) {
	// Reproduce the bug we found on a live node: hour names are bare
	// integers ("0".."23") without a leading zero. Lex order would put
	// "10" before "2", which means a naive sort would pick the wrong
	// "latest" file when the day crosses 10:00. Verify our sort uses
	// numeric order.
	root := t.TempDir()
	dateDir := filepath.Join(root, "20260525")
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create files for hours 1, 2, 9, 10, 11 — lex order would pick 9 last;
	// numeric order picks 11.
	for _, h := range []int{1, 2, 9, 10, 11} {
		path := filepath.Join(dateDir, strconv.Itoa(h))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := latestHourlyFile(root)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := "/20260525/11"
	if want := filepath.Join(dateDir, "11"); got != want {
		t.Errorf("got %q, want %q (suffix %q)", got, want, wantSuffix)
	}
}

func TestLatestHourlyFile_LatestDate(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"20260524", "20260525"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		// older date has hour 23, newer date has only hour 6
		hour := "23"
		if d == "20260525" {
			hour = "6"
		}
		if err := os.WriteFile(filepath.Join(root, d, hour), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := latestHourlyFile(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "20260525", "6")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLatestHourlyFile_MissingRoot(t *testing.T) {
	_, err := latestHourlyFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error on missing root")
	}
}

func TestParseVisorTime(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"2026-05-25T07:00:09.501967925", true}, // nanoseconds, no zone
		{"2026-05-25T07:00:09Z", true},
		{"2026-05-25T07:00:09.123Z", true},
		{"2026-05-25T07:00:09", true},
		{"", false},
		{"not a time", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, ok := parseVisorTime(c.in)
			if ok != c.wantOK {
				t.Errorf("ok=%v want %v for %q", ok, c.wantOK, c.in)
			}
		})
	}
}
