package monitors

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestScanTmpDirSeparatesReceiptsFromMaterialWithoutChangingLegacyTotals(t *testing.T) {
	root := t.TempDir()
	now := time.Now().Truncate(time.Second)
	old := now.Add(-48 * time.Hour)

	receiptOld := writeTmpFixture(t, root, "shell_rs_out/receipt-old", nil, old)
	_ = receiptOld
	writeTmpFixture(t, root, "shell_rs_out/receipt-recent", nil, now)
	writeTmpFixture(t, root, "shell_rs_out/material-old", []byte("four"), old)
	orphan := writeTmpFixture(t, root, "orphan-old", []byte("orphan"), old)
	writeTmpFixture(t, root, "recent", []byte("fresh"), now)
	writeTmpFixture(t, root, "zero-old", nil, old)
	symlink := filepath.Join(root, "unknown-symlink")
	if err := os.Symlink(orphan, symlink); err != nil {
		t.Fatal(err)
	}
	symlinkInfo, err := os.Lstat(symlink)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := scanTmpDir(root, now)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(len("four")+len("orphan")+len("fresh")) + symlinkInfo.Size()
	if snapshot.totalBytes != wantBytes || snapshot.staleFiles != 4 || snapshot.shellExecFiles != 3 {
		t.Fatalf("legacy snapshot = %+v, want bytes=%d stale=4 shell=3", snapshot, wantBytes)
	}
	if snapshot.filesByClass[tmpClassReceipt] != 2 || snapshot.bytesByClass[tmpClassReceipt] != 0 {
		t.Fatalf("receipt class = files:%d bytes:%d", snapshot.filesByClass[tmpClassReceipt], snapshot.bytesByClass[tmpClassReceipt])
	}
	if snapshot.filesByClass[tmpClassMaterial] != 5 || snapshot.bytesByClass[tmpClassMaterial] != wantBytes {
		t.Fatalf("material class = files:%d bytes:%d, want files=5 bytes=%d", snapshot.filesByClass[tmpClassMaterial], snapshot.bytesByClass[tmpClassMaterial], wantBytes)
	}
	if snapshot.materialStaleFiles != 2 || snapshot.materialStaleBytes != int64(len("four")+len("orphan")) {
		t.Fatalf("material stale = files:%d bytes:%d", snapshot.materialStaleFiles, snapshot.materialStaleBytes)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	empty, err := scanTmpDir(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.totalBytes != 0 || empty.staleFiles != 0 || empty.shellExecFiles != 0 ||
		empty.filesByClass[tmpClassReceipt] != 0 || empty.filesByClass[tmpClassMaterial] != 0 ||
		empty.materialStaleFiles != 0 || empty.materialStaleBytes != 0 {
		t.Fatalf("deletion did not clear staged snapshot: %+v", empty)
	}
}

func TestScanTmpDirRejectsPartialWalk(t *testing.T) {
	root := t.TempDir()
	file := writeTmpFixture(t, root, "partial", []byte("partial"), time.Now())
	walk := func(root string, callback fs.WalkDirFunc) error {
		if err := walkOnePath(root, callback); err != nil {
			return err
		}
		if err := walkOnePath(file, callback); err != nil {
			return err
		}
		return errors.New("injected partial walk")
	}
	snapshot, err := scanTmpDirWith(root, time.Now(), walk)
	if err == nil {
		t.Fatal("partial tmp walk was accepted")
	}
	if snapshot.filesByClass != nil || snapshot.totalBytes != 0 {
		t.Fatalf("partial tmp snapshot escaped: %+v", snapshot)
	}
}

func TestTmpReceiptClassificationRequiresEmptyRegularShellFile(t *testing.T) {
	cases := []struct {
		name       string
		underShell bool
		info       fs.FileInfo
		want       string
	}{
		{"empty shell regular", true, fixedFileInfo{mode: 0, size: 0}, tmpClassReceipt},
		{"nonempty shell regular", true, fixedFileInfo{mode: 0, size: 1}, tmpClassMaterial},
		{"empty elsewhere", false, fixedFileInfo{mode: 0, size: 0}, tmpClassMaterial},
		{"zero shell fifo", true, fixedFileInfo{mode: os.ModeNamedPipe, size: 0}, tmpClassMaterial},
		{"zero shell symlink", true, fixedFileInfo{mode: os.ModeSymlink, size: 0}, tmpClassMaterial},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := tmpFileClass(test.underShell, test.info); got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTickTmpDirRetainsFailureAndPublishesDeletion(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	writeTmpFixture(t, root, "shell_rs_out/receipt", nil, now.Add(-48*time.Hour))
	writeTmpFixture(t, root, "material", []byte("retained"), now.Add(-48*time.Hour))

	if !tickTmpDirWith(root, func() time.Time { return now }, scanTmpDir) {
		t.Fatal("initial tmp tick failed")
	}
	legacyBytes := hostMetricValue(t, metrics.HLNodeTmpBytes)
	materialBytes := hostMetricValue(t, metrics.HLNodeTmpBytesByClass.WithLabelValues(tmpClassMaterial))
	legacyStale := hostMetricValue(t, metrics.HLNodeTmpStaleFiles)
	if legacyStale != 2 {
		t.Fatalf("legacy stale count = %v, want receipt + material = 2", legacyStale)
	}

	failingScan := func(string, time.Time) (tmpDirSnapshot, error) {
		return tmpDirSnapshot{totalBytes: 999, materialStaleBytes: 999}, fs.ErrPermission
	}
	if tickTmpDirWith(root, func() time.Time { return now.Add(time.Minute) }, failingScan) {
		t.Fatal("failed tmp scan reported success")
	}
	if got := hostMetricValue(t, metrics.HLNodeTmpBytes); got != legacyBytes {
		t.Fatalf("failed scan replaced legacy bytes: got %v want %v", got, legacyBytes)
	}
	if got := hostMetricValue(t, metrics.HLNodeTmpBytesByClass.WithLabelValues(tmpClassMaterial)); got != materialBytes {
		t.Fatalf("failed scan replaced material bytes: got %v want %v", got, materialBytes)
	}
	if got := hostMetricValue(t, metrics.HLNodeTmpScanUp); got != 0 {
		t.Fatalf("failed scan up = %v", got)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !tickTmpDirWith(root, func() time.Time { return now.Add(2 * time.Minute) }, scanTmpDir) {
		t.Fatal("empty recovery tmp tick failed")
	}
	for name, metric := range map[string]float64{
		"legacy bytes":        hostMetricValue(t, metrics.HLNodeTmpBytes),
		"legacy stale":        hostMetricValue(t, metrics.HLNodeTmpStaleFiles),
		"shell receipts":      hostMetricValue(t, metrics.HLNodeShellExecPending),
		"receipt files":       hostMetricValue(t, metrics.HLNodeTmpFiles.WithLabelValues(tmpClassReceipt)),
		"material files":      hostMetricValue(t, metrics.HLNodeTmpFiles.WithLabelValues(tmpClassMaterial)),
		"material stale":      hostMetricValue(t, metrics.HLNodeTmpMaterialStaleFiles),
		"material stale byte": hostMetricValue(t, metrics.HLNodeTmpMaterialStaleBytes),
	} {
		if metric != 0 {
			t.Fatalf("%s after deletion = %v", name, metric)
		}
	}
	if got := hostMetricValue(t, metrics.HLNodeTmpScanUp); got != 1 {
		t.Fatalf("recovered scan up = %v", got)
	}
}

func writeTmpFixture(t *testing.T, root, rel string, content []byte, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

type fixedFileInfo struct {
	mode fs.FileMode
	size int64
}

func (fixedFileInfo) Name() string           { return "fixture" }
func (info fixedFileInfo) Size() int64       { return info.size }
func (info fixedFileInfo) Mode() fs.FileMode { return info.mode }
func (fixedFileInfo) ModTime() time.Time     { return time.Time{} }
func (fixedFileInfo) IsDir() bool            { return false }
func (fixedFileInfo) Sys() any               { return nil }
