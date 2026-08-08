package utils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mk(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLatestFlatFile(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, "20260702")
	want := mk(t, dir, "20260704")
	mk(t, dir, "20260703")
	if err := os.Mkdir(filepath.Join(dir, "99999999"), 0o755); err != nil {
		t.Fatal(err) // directories must be ignored even when lex-greater
	}
	got, err := LatestFlatFile(dir)
	if err != nil || got != want {
		t.Fatalf("LatestFlatFile = %q, %v; want %q", got, err, want)
	}
	if _, err := LatestFlatFile(t.TempDir()); err == nil {
		t.Fatal("empty dir should error")
	}
}

func TestLatestNumericFile(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, "999")
	want := mk(t, dir, "1000") // lex sort would wrongly pick 999
	mk(t, dir, "notanumber")
	got, err := LatestNumericFile(dir)
	if err != nil || got != want {
		t.Fatalf("LatestNumericFile = %q, %v; want %q", got, err, want)
	}

	// extension form, as used by periodic_abci_states
	dir2 := t.TempDir()
	mk(t, dir2, "599840000.rmp")
	want2 := mk(t, dir2, "599900000.rmp")
	got2, err := LatestNumericFile(dir2)
	if err != nil || got2 != want2 {
		t.Fatalf("LatestNumericFile(.rmp) = %q, %v; want %q", got2, err, want2)
	}
}

func TestLatestDateNumericFile(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "20260703", "100.rmp")
	want := mk(t, root, "20260704", "50.rmp")
	// pre-created empty date dir must be skipped, not treated as newest
	if err := os.MkdirAll(filepath.Join(root, "20260705"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := LatestDateNumericFile(root)
	if err != nil || got != want {
		t.Fatalf("LatestDateNumericFile = %q, %v; want %q", got, err, want)
	}
}

func TestLatestReplicaFile(t *testing.T) {
	root := t.TempDir()
	// old run with data
	mk(t, root, "2026-07-03T08:34:11", "20260703", "999")
	// newest run, newest date, numeric max 1000 > 999 lexicographically tricky
	mk(t, root, "2026-07-04T07:08:31", "20260704", "999")
	want := mk(t, root, "2026-07-04T07:08:31", "20260704", "1000")
	// pruned-to-skeleton newer-looking run must be skipped
	if err := os.MkdirAll(filepath.Join(root, "2026-07-04T09:00:00", "20260704"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := LatestReplicaFile(root)
	if err != nil || got != want {
		t.Fatalf("LatestReplicaFile = %q, %v; want %q", got, err, want)
	}
}

func TestLatestReplicaFileStrictDistinguishesUnavailableAndTraversalErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if path, rootMissing, err := LatestReplicaFileStrict(missing); path != "" || !rootMissing || err != nil {
		t.Fatalf("missing = %q, %t, %v; want root missing", path, rootMissing, err)
	}

	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "2026-08-08T00:00:00", "20260808"), 0o755); err != nil {
		t.Fatal(err)
	}
	if path, rootMissing, err := LatestReplicaFileStrict(empty); path != "" || rootMissing || err != nil {
		t.Fatalf("empty skeleton = %q, %t, %v; want valid empty", path, rootMissing, err)
	}

	root := t.TempDir()
	olderRun := filepath.Join(root, "2026-08-07T00:00:00")
	newerRun := filepath.Join(root, "2026-08-08T00:00:00")
	olderDate := filepath.Join(olderRun, "20260807")
	newerDate := filepath.Join(newerRun, "20260808")
	oldLeaf := mk(t, olderDate, "999")
	_ = oldLeaf
	want := mk(t, newerDate, "1000")
	if path, rootMissing, err := LatestReplicaFileStrict(root); path != want || rootMissing || err != nil {
		t.Fatalf("found = %q, %t, %v; want %q", path, rootMissing, err, want)
	}

	wantErr := errors.New("injected traversal failure")
	for name, blocked := range map[string]string{
		"newest run":  newerRun,
		"newest date": newerDate,
	} {
		t.Run(name, func(t *testing.T) {
			readDir := func(path string) ([]os.DirEntry, error) {
				if path == blocked {
					return nil, wantErr
				}
				return os.ReadDir(path)
			}
			path, rootMissing, err := latestReplicaFileStrictWith(root, readDir)
			if path != "" || rootMissing || !errors.Is(err, wantErr) {
				t.Fatalf("nested failure = %q, %t, %v; want error without older fallback", path, rootMissing, err)
			}
		})
	}
}
