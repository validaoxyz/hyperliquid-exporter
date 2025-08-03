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

var currentCommitHash string

func StartVersionMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		// run immediately on startup
		if err := updateVersionInfo(ctx, cfg); err != nil {
			errCh <- fmt.Errorf("version monitor error: %w", err)
		}

		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := updateVersionInfo(ctx, cfg); err != nil {
					errCh <- fmt.Errorf("version monitor error: %w", err)
				}
			}
		}
	}()
}

func updateVersionInfo(ctx context.Context, cfg config.Config) error {
	// create a temporary file for the binary copy
	tmpFile, err := os.CreateTemp("", "hl_node_*.tmp")
	if err != nil {
		return fmt.Errorf("error creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close() // close immediately, we'll open it for writing

	// ensure cleanup
	defer os.Remove(tmpPath)

	// copy the binary to the temp file
	source, err := os.Open(cfg.NodeBinary)
	if err != nil {
		return fmt.Errorf("error opening source binary: %w", err)
	}
	defer source.Close()

	dest, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("error opening temp file: %w", err)
	}
	defer dest.Close()

	if _, err := io.Copy(dest, source); err != nil {
		return fmt.Errorf("error copying binary: %w", err)
	}
	dest.Close() // Close before executing

	// make the temp file executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("error making temp file executable: %w", err)
	}

	// run version command on the temp copy
	cmd := exec.CommandContext(ctx, tmpPath, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running version command: %w", err)
	}

	versionOutput := out.String()
	parts := strings.Split(versionOutput, "|")
	if len(parts) >= 3 {
		commitLine := parts[0]
		date := strings.TrimSpace(parts[1])

		commitParts := strings.Split(commitLine, " ")
		if len(commitParts) >= 2 {
			currentCommitHash = strings.TrimSpace(commitParts[1])
		}

		metrics.SetSoftwareVersion(currentCommitHash, date)
		logger.InfoComponent("system", "Detected hl-node version: commit=%s, date=%s", currentCommitHash, date)
		return nil
	}

	return fmt.Errorf("unexpected version output format: %s", versionOutput)
}
