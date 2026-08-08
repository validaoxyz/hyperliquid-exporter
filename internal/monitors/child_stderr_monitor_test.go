package monitors

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestClassifyChildStderrRequiresExplicitEvidence(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"app hash", `computed app hash 0xabc does not match quorum value QuorumAppHash{...}`, "app_hash_mismatch"},
		{"hardfork evidence", `observed qc for newer hardfork, crashing`, "hardfork_upgrade"},
		{"sync overflow", `thread 'main' panicked: too many blocks to request in gossip forward_client_blocks`, "sync_overflow"},
		{"config", `CONFIGURATION ERROR: validator is not in node_ips`, "config_error"},
		{"network", `upstream connect error or disconnect/reset before headers`, "network"},
		{"explicit panic at", `thread 'main' panicked at 'attempt to add with overflow'`, "panic"},
		{"explicit panic colon", `thread 'main' panicked:\nstack backtrace`, "panic"},
		{"unrelated stderr", `failed to load optional cache; retrying`, "unknown"},
		{"word panic is insufficient", `panic reporting mode enabled`, "unknown"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyChildStderr([]byte(test.input)); got != test.want {
				t.Fatalf("classify() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestScanChildStderrDistinguishesStatesAndReclassifiesGrowth(t *testing.T) {
	root := t.TempDir()
	empty := writeChildStderrFixture(t, root, "20270724/0/empty", "")
	partial := writeChildStderrFixture(t, root, "20270724/1/growing", "child starting")
	truncated := writeChildStderrFixture(t, root, "20270724/2/large", strings.Repeat("x", childStderrHeadBytes+1))

	first, err := scanChildStderr(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertChildState(t, first[empty], childStderrStateEmpty, childStderrReasonNone)
	assertChildState(t, first[partial], childStderrStateReadable, childStderrReasonUnknown)
	assertChildState(t, first[truncated], childStderrStateTruncated, childStderrReasonUnknown)

	if err := os.WriteFile(partial, []byte("child starting\nthread 'main' panicked at src/main.rs:1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte("observed qc for newer hardfork"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := scanChildStderr(root, first)
	if err != nil {
		t.Fatal(err)
	}
	assertChildState(t, second[partial], childStderrStateReadable, childStderrReasonPanic)
	assertChildState(t, second[empty], childStderrStateReadable, "hardfork_upgrade")
	assertChildState(t, second[truncated], childStderrStateTruncated, childStderrReasonUnknown)
}

func TestScanChildStderrRetriesUnreadableAndRejectsPartialDirectoryScan(t *testing.T) {
	root := t.TempDir()
	path := writeChildStderrFixture(t, root, "20270724/0/artifact", "thread 'main' panicked at src/main.rs:1")
	readCalls := 0
	unreadable, err := scanChildStderrWith(root, nil, os.ReadDir, func(string, int) ([]byte, error) {
		readCalls++
		return nil, fs.ErrPermission
	})
	if err != nil {
		t.Fatal(err)
	}
	assertChildState(t, unreadable[path], childStderrStateUnreadable, childStderrReasonNone)

	recovered, err := scanChildStderrWith(root, unreadable, os.ReadDir, func(path string, limit int) ([]byte, error) {
		readCalls++
		return readHead(path, limit)
	})
	if err != nil {
		t.Fatal(err)
	}
	if readCalls != 2 {
		t.Fatalf("unreadable artifact was not retried: read calls=%d", readCalls)
	}
	assertChildState(t, recovered[path], childStderrStateReadable, childStderrReasonPanic)

	partialReadDir := func(path string) ([]os.DirEntry, error) {
		if filepath.Base(path) == "0" {
			return nil, errors.New("injected directory read failure")
		}
		return os.ReadDir(path)
	}
	if snapshot, err := scanChildStderrWith(root, recovered, partialReadDir, readHead); err == nil || snapshot != nil {
		t.Fatalf("partial directory scan escaped: snapshot=%v err=%v", snapshot, err)
	}
}

func TestTickChildStderrRetainsFailureAndPublishesPruning(t *testing.T) {
	root := t.TempDir()
	path := writeChildStderrFixture(t, root, "20270724/0/artifact", "computed app hash x does not match quorum")
	seen := map[string]*childStderrState{}
	now := time.Unix(1_800_000_000, 0)
	if !tickChildStderrWith(root, seen, scanChildStderr, func() time.Time { return now }) {
		t.Fatal("initial child-stderr scan failed")
	}
	if len(seen) != 1 || seen[path].reason != "app_hash_mismatch" {
		t.Fatalf("initial state = %+v", seen)
	}

	failingScan := func(string, map[string]*childStderrState) (map[string]*childStderrState, error) {
		return map[string]*childStderrState{"invented": {state: childStderrStateReadable, reason: childStderrReasonPanic}}, fs.ErrPermission
	}
	if tickChildStderrWith(root, seen, failingScan, func() time.Time { return now.Add(time.Minute) }) {
		t.Fatal("failed child-stderr scan reported success")
	}
	if len(seen) != 1 || seen[path].reason != "app_hash_mismatch" {
		t.Fatalf("failed scan replaced last complete state: %+v", seen)
	}
	if got := hostMetricValue(t, metrics.HLNodeChildStderrScanUp); got != 0 {
		t.Fatalf("failed scan up = %v", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !tickChildStderrWith(root, seen, scanChildStderr, func() time.Time { return now.Add(2 * time.Minute) }) {
		t.Fatal("pruning recovery scan failed")
	}
	if len(seen) != 0 {
		t.Fatalf("pruned artifact remains: %+v", seen)
	}
	if got := hostMetricValue(t, metrics.HLNodeChildStarts); got != 0 {
		t.Fatalf("child starts after pruning = %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeChildStderrArtifacts.WithLabelValues(childStderrStateReadable, "app_hash_mismatch")); got != 0 {
		t.Fatalf("pruned artifact series = %v", got)
	}
}

func TestChildStderrSeriesCensusIsFiniteAndSemanticallyValid(t *testing.T) {
	series := childStderrSeriesCensus()
	if len(series) != 16 {
		t.Fatalf("series census = %d, want 16", len(series))
	}
	seen := make(map[childStderrSeries]struct{}, len(series))
	for _, item := range series {
		if _, duplicate := seen[item]; duplicate {
			t.Fatalf("duplicate series: %+v", item)
		}
		seen[item] = struct{}{}
		switch item.state {
		case childStderrStateEmpty, childStderrStateUnreadable:
			if item.reason != childStderrReasonNone {
				t.Fatalf("state %q has impossible reason %q", item.state, item.reason)
			}
		case childStderrStateReadable, childStderrStateTruncated:
			if item.reason == childStderrReasonNone {
				t.Fatalf("state %q has evidence-free none reason", item.state)
			}
		default:
			t.Fatalf("unbounded state %q", item.state)
		}
	}
}

func writeChildStderrFixture(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertChildState(t *testing.T, got *childStderrState, state, reason string) {
	t.Helper()
	if got == nil || got.state != state || got.reason != reason {
		t.Fatalf("child state = %+v, want state=%q reason=%q", got, state, reason)
	}
}
