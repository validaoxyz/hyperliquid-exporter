package monitors

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestScanSnapshotStatusUsesExactlyTwoNewestDateDirectories(t *testing.T) {
	root := t.TempDir()
	for _, date := range []string{"20260524", "20260525", "20260526"} {
		if err := os.Mkdir(filepath.Join(root, date), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteHeight(t, filepath.Join(root, "20260524"), 999_000_000)
	mustWriteHeight(t, filepath.Join(root, "20260525"), 1_000_000_000)
	latestTime := time.Date(2026, 5, 26, 3, 4, 5, 0, time.UTC)
	mustWriteHeightWithMtime(t, filepath.Join(root, "20260526"), 1_000_020_000, latestTime)

	got, err := scanSnapshotStatus(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.DateDirs != 2 || got.Known != 2 || got.LatestHeight != 1_000_020_000 || !got.LatestComplete.Equal(latestTime) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestScanSnapshotStatusRejectsPartialWindow(t *testing.T) {
	root := t.TempDir()
	date := filepath.Join(root, "20260526")
	if err := os.Mkdir(date, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteHeight(t, date, 1_000_020_000)
	if err := os.WriteFile(filepath.Join(date, "garbage"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanSnapshotStatus(root)
	if !errors.Is(err, errInvalidSnapshotStatusEntry) || got != (snapshotStatusSnapshot{}) {
		t.Fatalf("partial snapshot escaped: %+v, %v", got, err)
	}
}

func TestScanSnapshotStatusLateDiscoveryAndEmptyWindow(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "periodic_abci_state_statuses")
	if _, err := scanSnapshotStatus(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root error = %v", err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := scanSnapshotStatus(root)
	if err != nil || got != (snapshotStatusSnapshot{}) {
		t.Fatalf("late empty root = %+v, %v", got, err)
	}
	date := filepath.Join(root, "20260526")
	if err := os.Mkdir(date, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteHeight(t, date, 1_000_020_000)
	got, err = scanSnapshotStatus(root)
	if err != nil || got.Known != 1 || got.LatestHeight != 1_000_020_000 {
		t.Fatalf("late populated root = %+v, %v", got, err)
	}
}

func mustWriteHeight(t *testing.T, dir string, height int64) {
	t.Helper()
	mustWriteHeightWithMtime(t, dir, height, time.Time{})
}

func mustWriteHeightWithMtime(t *testing.T, dir string, height int64, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, strconv.FormatInt(height, 10))
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}
