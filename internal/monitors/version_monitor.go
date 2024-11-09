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

const (
	currentBinaryPath = "/tmp/hl_node_current"
)

var currentCommitHash string

func StartVersionMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
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

func copyBinary(src string) error {
	source, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("error opening source binary: %w", err)
	}
	defer source.Close()

	dest, err := os.OpenFile(currentBinaryPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("error creating destination binary: %w", err)
	}
	defer dest.Close()

	if _, err := io.Copy(dest, source); err != nil {
		return fmt.Errorf("error copying binary: %w", err)
	}

	return nil
}

func updateVersionInfo(ctx context.Context, cfg config.Config) error {
	// Copy binary to temp location
	if err := copyBinary(cfg.NodeBinary); err != nil {
		return fmt.Errorf("error copying binary: %w", err)
	}

	cmd := exec.CommandContext(ctx, currentBinaryPath, "--version")
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
		logger.Info("Updated software version: commit=%s, date=%s", currentCommitHash, date)
		return nil
	}

	return fmt.Errorf("unexpected version output format: %s", versionOutput)
}
