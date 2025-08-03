package monitors

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	downloadInterval = 30 * time.Minute
)

var (
	lastDownloadTime time.Time
	cachedLatestHash string
	cacheExpiry      time.Time
)

func StartUpdateChecker(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		// wait a bit for version monitor to run first
		time.Sleep(2 * time.Second)

		// run immediately on startup
		if err := checkSoftwareUpdate(ctx, cfg); err != nil {
			errCh <- fmt.Errorf("update checker error: %w", err)
		}

		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := checkSoftwareUpdate(ctx, cfg); err != nil {
					errCh <- fmt.Errorf("update checker error: %w", err)
				}
			}
		}
	}()
}

func checkSoftwareUpdate(ctx context.Context, cfg config.Config) error {
	// use cached value if still valid
	if time.Now().Before(cacheExpiry) && cachedLatestHash != "" {
		updateUpToDateStatus()
		return nil
	}

	// determine binary URL based on chain
	var binaryURL string
	if cfg.Chain == "mainnet" {
		binaryURL = "https://binaries.hyperliquid.xyz/Mainnet/hl-visor"
	} else {
		binaryURL = "https://binaries.hyperliquid-testnet.xyz/Testnet/hl-visor"
	}

	// create temporary file for download
	tmpFile, err := os.CreateTemp("", "hl-visor-*.tmp")
	if err != nil {
		return fmt.Errorf("error creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	// ensure cleanup
	defer os.Remove(tmpPath)

	// download binary to temp file
	downloadCmd := exec.CommandContext(ctx, "curl", "-sSL", "-o", tmpPath, binaryURL)
	if err := downloadCmd.Run(); err != nil {
		return fmt.Errorf("error downloading latest binary: %w", err)
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("error changing permissions: %w", err)
	}

	// get version of the downloaded binary
	versionCmd := exec.CommandContext(ctx, tmpPath, "--version")
	var out bytes.Buffer
	versionCmd.Stdout = &out

	if err := versionCmd.Run(); err != nil {
		return fmt.Errorf("error running version command: %w", err)
	}

	latestVersionOutput := out.String()
	parts := strings.Split(latestVersionOutput, "|")
	if len(parts) >= 3 {
		commitLine := parts[0]
		latestCommitParts := strings.Split(commitLine, " ")
		if len(latestCommitParts) >= 2 {
			cachedLatestHash = strings.TrimSpace(latestCommitParts[1])
			cacheExpiry = time.Now().Add(downloadInterval)

			updateUpToDateStatus()
			return nil
		}
	}

	return fmt.Errorf("unexpected version output format: %s", latestVersionOutput)
}

func updateUpToDateStatus() {
	if currentCommitHash == "" {
		// version monitor hasn't run yet, skip
		return
	}

	if currentCommitHash == cachedLatestHash {
		metrics.SetSoftwareUpToDate(true)
	} else {
		metrics.SetSoftwareUpToDate(false)
		logger.InfoComponent("system", "Software is NOT up to date. Current: %s, Latest: %s",
			currentCommitHash, cachedLatestHash)
	}
}
