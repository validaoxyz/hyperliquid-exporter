package monitors

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	tmpDirPollInterval = 5 * time.Minute
	tmpDirStaleAge     = 24 * time.Hour
)

const (
	tmpClassReceipt  = "receipt"
	tmpClassMaterial = "material"
)

var tmpClasses = []string{tmpClassReceipt, tmpClassMaterial}

type tmpDirSnapshot struct {
	totalBytes         int64
	staleFiles         int64
	shellExecFiles     int64
	filesByClass       map[string]int64
	bytesByClass       map[string]int64
	materialStaleFiles int64
	materialStaleBytes int64
}

// StartTmpDirMonitor walks $NODE_HOME/tmp once every 5 minutes and publishes
// compatibility totals plus receipt/material classes. Material stale bytes
// and files catch orphaned writes from past crashes — on the
// test peer we observed ~2.5 GB of stale tmp files dating to May/Jul
// 2025, which is what an alert would have caught.
func StartTmpDirMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "tmp")
	metrics.RegisterSource(metrics.SourceTmpDir, true)

	logger.InfoComponent("tmp_dir", "watching %s every %s", root, tmpDirPollInterval)

	ticker := time.NewTicker(tmpDirPollInterval)
	defer ticker.Stop()

	tickTmpDir(root)
	metrics.MarkMonitorTick("tmp_dir")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickTmpDir(root)
			metrics.MarkMonitorTick("tmp_dir")
		}
	}
}

func tickTmpDir(root string) {
	tickTmpDirWith(root, time.Now, scanTmpDir)
}

type tmpDirScanFunc func(string, time.Time) (tmpDirSnapshot, error)

func tickTmpDirWith(root string, now func() time.Time, scan tmpDirScanFunc) bool {
	metrics.MarkMonitorAttempt("tmp_dir")
	metrics.MarkSourceAttempt(metrics.SourceTmpDir)
	observedAt := now()
	snapshot, err := scan(root, observedAt)
	if err != nil {
		metrics.HLNodeTmpScanUp.Set(0)
		if errors.Is(err, fs.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceTmpDir)
		} else {
			metrics.MarkSourceError(metrics.SourceTmpDir, metrics.SourceFailureWalk)
		}
		logger.DebugComponent("tmp_dir", "tmp walk incomplete; retaining last complete snapshot: %v", err)
		return false
	}

	metrics.HLNodeTmpBytes.Set(float64(snapshot.totalBytes))
	metrics.HLNodeTmpStaleFiles.Set(float64(snapshot.staleFiles))
	metrics.HLNodeShellExecPending.Set(float64(snapshot.shellExecFiles))
	for _, class := range tmpClasses {
		metrics.HLNodeTmpFiles.WithLabelValues(class).Set(float64(snapshot.filesByClass[class]))
		metrics.HLNodeTmpBytesByClass.WithLabelValues(class).Set(float64(snapshot.bytesByClass[class]))
	}
	metrics.HLNodeTmpMaterialStaleFiles.Set(float64(snapshot.materialStaleFiles))
	metrics.HLNodeTmpMaterialStaleBytes.Set(float64(snapshot.materialStaleBytes))
	metrics.HLNodeTmpScanUp.Set(1)
	completedAt := now()
	metrics.HLNodeTmpLastCompleteTimestampSeconds.Set(float64(completedAt.Unix()))
	metrics.MarkSourceValidObservation(metrics.SourceTmpDir, time.Time{})
	metrics.MarkSourcePublication(metrics.SourceTmpDir)
	metrics.MarkMonitorValidObservation("tmp_dir")
	metrics.MarkMonitorPublication("tmp_dir")
	return true
}

func scanTmpDir(root string, now time.Time) (tmpDirSnapshot, error) {
	return scanTmpDirWith(root, now, filepath.WalkDir)
}

type tmpWalkDirFunc func(string, fs.WalkDirFunc) error

func scanTmpDirWith(root string, now time.Time, walk tmpWalkDirFunc) (tmpDirSnapshot, error) {
	snapshot := tmpDirSnapshot{
		filesByClass: map[string]int64{
			tmpClassReceipt:  0,
			tmpClassMaterial: 0,
		},
		bytesByClass: map[string]int64{
			tmpClassReceipt:  0,
			tmpClassMaterial: 0,
		},
	}
	threshold := now.Add(-tmpDirStaleAge)
	shellExecDir := filepath.Join(root, "shell_rs_out")
	err := walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		snapshot.totalBytes += size
		if info.ModTime().Before(threshold) {
			snapshot.staleFiles++
		}
		// Sub-bucket: count files under tmp/shell_rs_out/ separately.
		// Each visor shell-exec drops a (usually empty) file here; a
		// healthy node should keep this count low. Sustained growth
		// means the visor's cleanup pass is broken.
		underShellExec := strings.HasPrefix(path, shellExecDir+string(filepath.Separator))
		if underShellExec {
			snapshot.shellExecFiles++
		}
		class := tmpFileClass(underShellExec, info)
		snapshot.filesByClass[class]++
		snapshot.bytesByClass[class] += size
		if class == tmpClassMaterial && size > 0 && info.ModTime().Before(threshold) {
			snapshot.materialStaleFiles++
			snapshot.materialStaleBytes += size
		}
		return nil
	})
	if err != nil {
		return tmpDirSnapshot{}, err
	}
	return snapshot, nil
}

func tmpFileClass(underShellExec bool, info fs.FileInfo) string {
	if underShellExec && info.Mode().IsRegular() && info.Size() == 0 {
		return tmpClassReceipt
	}
	return tmpClassMaterial
}
