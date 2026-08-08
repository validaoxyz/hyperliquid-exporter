package monitors

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanReplayUsesEncodedStartAndSeparatesActivity(t *testing.T) {
	root := t.TempDir()
	oldName := "640000000_2026-08-04T00:50:57Z"
	newName := "643440100_2026-08-07T09:11:57Z"
	oldDir := filepath.Join(root, oldName)
	newDir := filepath.Join(root, newName)
	for _, dir := range []string{oldDir, newDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	retouched := time.Date(2026, 8, 8, 17, 17, 0, 0, time.UTC)
	mustChtime(t, oldDir, retouched)
	mustChtime(t, newDir, time.Date(2026, 8, 7, 9, 12, 0, 0, time.UTC))

	got, err := scanReplay(root)
	if err != nil {
		t.Fatal(err)
	}
	wantStart, _ := time.Parse(time.RFC3339Nano, "2026-08-07T09:11:57Z")
	if got.Retained != 2 || got.LatestHeight != 643440100 || !got.LatestStart.Equal(wantStart) || !got.LatestActivity.Equal(retouched) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestScanReplayPruningAndInvalidEntry(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "643440100_2026-08-07T09:11:57Z")
	if err := os.Mkdir(valid, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := scanReplay(root)
	if err != nil || got.Retained != 1 {
		t.Fatalf("valid snapshot = %+v, %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(root, "bad_marker"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = scanReplay(root)
	if !errors.Is(err, errInvalidReplayEntry) || got != (replaySnapshot{}) {
		t.Fatalf("invalid snapshot escaped: %+v, %v", got, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "bad_marker")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(valid); err != nil {
		t.Fatal(err)
	}
	got, err = scanReplay(root)
	if err != nil || got != (replaySnapshot{}) {
		t.Fatalf("empty retained window = %+v, %v", got, err)
	}
}
