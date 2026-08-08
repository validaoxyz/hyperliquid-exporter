package monitors

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var (
	currentCommitHash   string
	lastNodeBinaryMtime time.Time
)

// NodeBinaryReady reports whether the configured hl-node binary is present
// and looks executable. The exporter calls this at startup to decide
// whether to launch the version monitor; if the binary isn't there we
// no-op rather than spam errors every 30 minutes. Returns nil if ready.
func NodeBinaryReady(cfg config.Config) (string, error) {
	info, err := os.Stat(cfg.NodeBinary)
	if err != nil {
		return cfg.NodeBinary, fmt.Errorf("hl-node binary not found at %s: %w", cfg.NodeBinary, err)
	}
	if info.IsDir() {
		return cfg.NodeBinary, fmt.Errorf("hl-node path is a directory: %s", cfg.NodeBinary)
	}
	if info.Mode()&0111 == 0 {
		return cfg.NodeBinary, fmt.Errorf("hl-node at %s is not executable", cfg.NodeBinary)
	}
	return cfg.NodeBinary, nil
}

func StartVersionMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceVersion, true)
	goSafe("version", func() {
		// run immediately on startup
		if err := updateVersionInfo(ctx, cfg); err != nil {
			ReportError(ctx, "version", errCh, fmt.Errorf("version monitor error: %w", err))
		}
		metrics.MarkMonitorTick("version")

		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := updateVersionInfo(ctx, cfg); err != nil {
					ReportError(ctx, "version", errCh, fmt.Errorf("version monitor error: %w", err))
				}
				metrics.MarkMonitorTick("version")
			}
		}
	})
}

func updateVersionInfo(ctx context.Context, cfg config.Config) error {
	info, err := os.Stat(cfg.NodeBinary)
	if err != nil {
		return fmt.Errorf("error stating node binary: %w", err)
	}

	// the binary only changes when hl-visor swaps it; skip the copy+exec
	// when the mtime hasn't moved since the last successful probe
	if currentCommitHash != "" && !info.ModTime().After(lastNodeBinaryMtime) {
		return nil
	}

	commit, date, err := binaryVersionViaCopy(ctx, cfg.NodeBinary)
	if err != nil {
		return err
	}

	currentCommitHash = commit
	lastNodeBinaryMtime = info.ModTime()
	metrics.SetSoftwareVersion(currentCommitHash, date)
	logger.InfoComponent("system", "Detected hl-node version: commit=%s, date=%s", currentCommitHash, date)
	return nil
}

// binaryVersionViaCopy copies the binary to a temp file before running
// `--version` on it, so hl-visor atomically swapping the real binary
// mid-exec can't bite us. Use for live binaries; for files nothing else
// touches (e.g. a fresh download) call binaryVersionDirect.
func binaryVersionViaCopy(ctx context.Context, path string) (commit, date string, err error) {
	tmpFile, err := os.CreateTemp("", "hl_version_*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("error creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	source, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("error opening source binary: %w", err)
	}
	defer source.Close()

	dest, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return "", "", fmt.Errorf("error opening temp file: %w", err)
	}
	if _, err := io.Copy(dest, source); err != nil {
		dest.Close()
		return "", "", fmt.Errorf("error copying binary: %w", err)
	}
	dest.Close()

	return binaryVersionDirect(ctx, tmpPath)
}

// binaryVersionDirect runs `<path> --version` and parses the
// "commit <hash> | <date> | ..." output shared by hl-node and hl-visor.
func binaryVersionDirect(ctx context.Context, path string) (commit, date string, err error) {
	if err := os.Chmod(path, 0755); err != nil {
		return "", "", fmt.Errorf("error making binary executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, path, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("error running version command: %w", err)
	}

	parts := strings.Split(out.String(), "|")
	if len(parts) < 3 {
		return "", "", fmt.Errorf("unexpected version output format: %s", out.String())
	}

	commitParts := strings.Split(parts[0], " ")
	if len(commitParts) < 2 {
		return "", "", fmt.Errorf("unexpected version output format: %s", out.String())
	}
	return strings.TrimSpace(commitParts[1]), strings.TrimSpace(parts[1]), nil
}
