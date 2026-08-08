package monitors

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestTrackedDiskPathsHaveExactFrozenCensus(t *testing.T) {
	want := []string{
		"data/replica_cmds",
		"data/evm_block_and_receipts",
		"data/block_times",
		"data/node_fast_block_times",
		"data/node_slow_block_times",
		"data/node_logs",
		"data/latency_buckets",
		"data/latency_summaries",
		"data/periodic_abci_states",
		"data/visor_abci_states",
		"data/tcp_traffic",
		"data/dhs",
		"hyperliquid_data",
		"hyperliquid_data/db_hub/Evm",
		"hyperliquid_data/db_hub/Exchange",
		"hyperliquid_data/db_hub/Rpc",
		"hyperliquid_data/evm_db_hub_fast",
		"hyperliquid_data/evm_db_hub_fast/EvmState",
		"hyperliquid_data/evm_db_hub_slow",
		"hyperliquid_data/evm_db_hub_slow/EvmState",
		"hyperliquid_data/evm_db_hub_slow/checkpoint",
		"tmp",
	}
	if !reflect.DeepEqual(trackedSubdirs, want) {
		t.Fatalf("tracked paths changed:\n got %q\nwant %q", trackedSubdirs, want)
	}
}

func TestDiskPathStateLifecycle(t *testing.T) {
	root := t.TempDir()
	const rel = "hyperliquid_data/evm_db_hub_fast/EvmState"
	path := filepath.Join(root, filepath.FromSlash(rel))
	assertState := func(want string) {
		t.Helper()
		snapshot, err := walkSizes(root, []string{rel})
		if err != nil {
			t.Fatal(err)
		}
		if got := snapshot.pathState[rel]; got != want {
			t.Fatalf("path state = %q, want %q", got, want)
		}
	}
	assertState(diskPathAbsent)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	assertState(diskPathPresentEmpty)
	if err := os.WriteFile(filepath.Join(path, "state"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertState(diskPathPresentNonempty)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	assertState(diskPathAbsent)
}

func TestWalkSizesSeparatesApparentAndUniqueAllocatedBytes(t *testing.T) {
	if !diskAllocatedBytesSupported() {
		t.Skip("allocated-byte identity is unavailable on this platform")
	}
	root := t.TempDir()
	for _, rel := range []string{
		"data/replica_cmds",
		"data/block_times",
		"data/node_slow_block_times/nested",
		"tmp",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	original := filepath.Join(root, "data", "replica_cmds", "sst")
	contents := bytes.Repeat([]byte{0x5a}, 8192)
	if err := os.WriteFile(original, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(root, "data", "replica_cmds", "sst-hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(root, "tmp", "sst-hardlink")); err != nil {
		t.Fatal(err)
	}
	sparse := filepath.Join(root, "tmp", "sparse")
	file, err := os.Create(sparse)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "node_slow_block_times", "sample"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := walkSizes(root, trackedSubdirs)
	if err != nil {
		t.Fatal(err)
	}
	wantApparent := int64(3*len(contents) + (1 << 20) + 1)
	if snapshot.apparentTotal != wantApparent {
		t.Fatalf("apparent total = %d, want %d", snapshot.apparentTotal, wantApparent)
	}
	if got, want := snapshot.apparentByPath["data/replica_cmds"], int64(2*len(contents)); got != want {
		t.Fatalf("replica apparent = %d, want %d", got, want)
	}
	if got, want := snapshot.apparentByPath["tmp"], int64(len(contents)+(1<<20)); got != want {
		t.Fatalf("tmp apparent = %d, want %d", got, want)
	}

	if got, want := snapshot.allocatedTotal, referenceAllocatedBytes(t, root, root); got != want {
		t.Fatalf("allocated total = %d, want reference %d", got, want)
	}
	for _, rel := range []string{"data/replica_cmds", "tmp"} {
		scope := filepath.Join(root, filepath.FromSlash(rel))
		if got, want := snapshot.allocatedByPath[rel], referenceAllocatedBytes(t, root, scope); got != want {
			t.Fatalf("%s allocated = %d, want reference %d", rel, got, want)
		}
	}
	// The same hardlinked inode is unique within each scope, so it contributes
	// once to replica_cmds and once to tmp, but only once to the root total.
	originalInfo, err := os.Lstat(original)
	if err != nil {
		t.Fatal(err)
	}
	_, _, originalAllocated, ok := allocatedFileInfo(originalInfo)
	if !ok {
		t.Fatal("original file has no allocation identity")
	}
	if originalAllocated > 0 && snapshot.allocatedByPath["data/replica_cmds"] < originalAllocated {
		t.Fatal("replica scope omitted the hardlinked allocation")
	}
	if originalAllocated > 0 && snapshot.allocatedByPath["tmp"] < originalAllocated {
		t.Fatal("tmp scope omitted the cross-scope hardlink allocation")
	}

	if got := snapshot.pathState["data/block_times"]; got != diskPathPresentEmpty {
		t.Fatalf("empty path state = %q", got)
	}
	if got := snapshot.pathState["data/node_slow_block_times"]; got != diskPathPresentNonempty {
		t.Fatalf("populated path state = %q", got)
	}
	if got := snapshot.pathState["data/node_fast_block_times"]; got != diskPathAbsent {
		t.Fatalf("absent path state = %q", got)
	}
}

func TestWalkSizesRejectsOuterCallbackAndMetadataFailures(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "partial")
	if err := os.WriteFile(file, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	outerFailure := func(root string, callback fs.WalkDirFunc) error {
		if err := walkOnePath(root, callback); err != nil {
			return err
		}
		if err := walkOnePath(file, callback); err != nil {
			return err
		}
		return errors.New("injected outer walk failure")
	}
	if snapshot, err := walkSizesWith(root, []string{"partial"}, outerFailure); err == nil || snapshot.apparentByPath != nil {
		t.Fatalf("partial outer walk published %+v, err=%v", snapshot, err)
	}

	callbackFailure := func(root string, callback fs.WalkDirFunc) error {
		if err := walkOnePath(root, callback); err != nil {
			return err
		}
		return callback(filepath.Join(root, "denied"), nil, fs.ErrPermission)
	}
	if snapshot, err := walkSizesWith(root, []string{"partial"}, callbackFailure); err == nil || snapshot.apparentByPath != nil {
		t.Fatalf("callback failure published %+v, err=%v", snapshot, err)
	}

	infoFailure := func(root string, callback fs.WalkDirFunc) error {
		if err := walkOnePath(root, callback); err != nil {
			return err
		}
		return callback(filepath.Join(root, "bad-info"), failingDirEntry{name: "bad-info"}, nil)
	}
	if snapshot, err := walkSizesWith(root, []string{"partial"}, infoFailure); err == nil || snapshot.apparentByPath != nil {
		t.Fatalf("metadata failure published %+v, err=%v", snapshot, err)
	}
}

func TestAllocatedIdentityDistinguishesDevicesAndDeduplicatesHardlinks(t *testing.T) {
	seen := make(map[diskFileID]struct{})
	var total int64
	total += addUniqueAllocated(seen, diskFileID{device: 1, inode: 42}, 100)
	total += addUniqueAllocated(seen, diskFileID{device: 1, inode: 42}, 100)
	total += addUniqueAllocated(seen, diskFileID{device: 2, inode: 42}, 100)
	if total != 200 {
		t.Fatalf("cross-device dedupe total = %d, want 200", total)
	}
}

func TestTickDiskRetainsCompleteSnapshotOnFailureAndRecovers(t *testing.T) {
	root := t.TempDir()
	tracked := filepath.Join(root, "data", "block_times")
	if err := os.MkdirAll(tracked, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tracked, "sample")
	if err := os.WriteFile(file, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	diskLastCompleteUnix.Store(0)
	t.Cleanup(func() { diskLastCompleteUnix.Store(0) })

	firstTime := time.Unix(1_800_000_000, 0)
	statOne := func(string) (*fsStats, error) { return &fsStats{Bavail: 10, Blocks: 20, Bsize: 4096}, nil }
	if !tickDiskWith(root, statOne, walkSizes, func() time.Time { return firstTime }) {
		t.Fatal("initial complete disk tick failed")
	}
	used := hostMetricValue(t, metrics.HLNodeDiskUsedBytes)
	allocated := hostMetricValue(t, metrics.HLNodeDiskAllocatedBytes)
	if got := hostMetricValue(t, metrics.HLNodeDiskPathState.WithLabelValues("data/block_times", diskPathPresentNonempty)); got != 1 {
		t.Fatalf("initial path state = %v", got)
	}

	statTwo := func(string) (*fsStats, error) { return &fsStats{Bavail: 5, Blocks: 20, Bsize: 4096}, nil }
	failingWalk := func(string, []string) (diskSnapshot, error) {
		return diskSnapshot{apparentTotal: 999999, allocatedTotal: 999999}, fs.ErrPermission
	}
	failedAt := firstTime.Add(37 * time.Second)
	if tickDiskWith(root, statTwo, failingWalk, func() time.Time { return failedAt }) {
		t.Fatal("incomplete disk tick reported success")
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskUsedBytes); got != used {
		t.Fatalf("failed walk replaced apparent bytes: got %v want %v", got, used)
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskAllocatedBytes); got != allocated {
		t.Fatalf("failed walk replaced allocated bytes: got %v want %v", got, allocated)
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskWalkUp); got != 0 {
		t.Fatalf("failed walk up = %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskLastCompleteAgeSeconds); got != 37 {
		t.Fatalf("last complete age = %v, want 37", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskFreeBytes); got != 5*4096 {
		t.Fatalf("independent statfs free bytes = %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskPathState.WithLabelValues("data/block_times", diskPathPresentNonempty)); got != 1 {
		t.Fatalf("failed walk replaced path state: %v", got)
	}

	if err := os.WriteFile(file, []byte("recovered-and-larger"), 0o600); err != nil {
		t.Fatal(err)
	}
	recoveredAt := failedAt.Add(time.Second)
	if !tickDiskWith(root, statTwo, walkSizes, func() time.Time { return recoveredAt }) {
		t.Fatal("recovery disk tick failed")
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskUsedBytes); got == used {
		t.Fatalf("recovery did not publish changed apparent bytes: %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeDiskWalkUp); got != 1 {
		t.Fatalf("recovered walk up = %v", got)
	}
}

func referenceAllocatedBytes(t *testing.T, root, scope string) int64 {
	t.Helper()
	seen := make(map[diskFileID]struct{})
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != scope && !strings.HasPrefix(path, scope+string(filepath.Separator)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		device, inode, allocated, ok := allocatedFileInfo(info)
		if !ok {
			return errors.New("missing allocation identity")
		}
		id := diskFileID{device: device, inode: inode}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			total += allocated
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func walkOnePath(path string, callback fs.WalkDirFunc) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return callback(path, fs.FileInfoToDirEntry(info), nil)
}

type failingDirEntry struct{ name string }

func (entry failingDirEntry) Name() string         { return entry.name }
func (failingDirEntry) IsDir() bool                { return false }
func (failingDirEntry) Type() fs.FileMode          { return 0 }
func (failingDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrPermission }
