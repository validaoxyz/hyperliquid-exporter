package monitors

import (
	"context"
	"io/fs"
	"os"
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

// StartTmpDirMonitor walks $NODE_HOME/tmp once every 5 minutes and
// publishes two gauges: total byte size and a count of files older than
// 24h. The latter catches orphaned writes from past crashes — on the
// test peer we observed ~2.5 GB of stale tmp files dating to May/Jul
// 2025, which is what an alert would have caught.
func StartTmpDirMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "tmp")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("tmp_dir",
			"$NODE_HOME/tmp not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

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
	var total int64
	var stale int64
	var shellExec int64
	threshold := time.Now().Add(-tmpDirStaleAge)
	shellExecDir := filepath.Join(root, "shell_rs_out")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if info.ModTime().Before(threshold) {
			stale++
		}
		// Sub-bucket: count files under tmp/shell_rs_out/ separately.
		// Each visor shell-exec drops a (usually empty) file here; a
		// healthy node should keep this count low. Sustained growth
		// means the visor's cleanup pass is broken.
		if strings.HasPrefix(path, shellExecDir+string(filepath.Separator)) || path == shellExecDir {
			shellExec++
		}
		return nil
	})
	metrics.HLNodeTmpBytes.Set(float64(total))
	metrics.HLNodeTmpStaleFiles.Set(float64(stale))
	metrics.HLNodeShellExecPending.Set(float64(shellExec))
}
