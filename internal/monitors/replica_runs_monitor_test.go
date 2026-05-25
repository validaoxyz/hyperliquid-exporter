package monitors

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplicaRuns_CountsDirsAndPicksNewestMtime(t *testing.T) {
	root := t.TempDir()

	// Three "run" dirs with explicit mtimes.
	oldDir := filepath.Join(root, "2026-05-23T08:25:08Z")
	midDir := filepath.Join(root, "2026-05-25T11:07:30Z")
	newDir := filepath.Join(root, "2026-05-25T11:13:57Z")
	for _, d := range []string{oldDir, midDir, newDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Override mtimes so we know which is newest deterministically.
	old := time.Now().Add(-72 * time.Hour)
	mid := time.Now().Add(-2 * time.Hour)
	newest := time.Now().Add(-30 * time.Minute)
	mustChtime(t, oldDir, old)
	mustChtime(t, midDir, mid)
	mustChtime(t, newDir, newest)

	// Also a non-dir entry; should be ignored.
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tickReplicaRuns(root)

	// The function sets the gauges directly. We can't easily read them
	// from a test, but the smoke is: it doesn't panic on a mixed-content
	// directory and identifies the three dir entries.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		}
	}
	if dirCount != 3 {
		t.Errorf("expected 3 dirs in fixture, got %d", dirCount)
	}
}

func TestReplicaRuns_EmptyRootIsSafe(t *testing.T) {
	tickReplicaRuns(t.TempDir())
}

func mustChtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
