package monitors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	binaryURL        = "https://binaries.hyperliquid.xyz/Testnet/hl-visor"
	localBinaryPath  = "/tmp/hl-visor-latest"
	downloadInterval = 5 * time.Minute
)

var lastDownloadTime time.Time

func StartUpdateChecker(ctx context.Context, errCh chan<- error) {
	go func() {
		ticker := time.NewTicker(300 * time.Second) // 5 minutes
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := checkSoftwareUpdate(ctx); err != nil {
					errCh <- fmt.Errorf("update checker error: %w", err)
				}
			}
		}
	}()
}

func shouldDownloadNewBinary(ctx context.Context) (bool, error) {
	// Check if we have a recent download
	if time.Since(lastDownloadTime) < downloadInterval {
		return false, nil
	}

	// Check if file exists
	if _, err := os.Stat(localBinaryPath); os.IsNotExist(err) {
		return true, nil
	}

	// Get remote file hash
	resp, err := http.Head(binaryURL)
	if err != nil {
		return false, fmt.Errorf("error checking remote binary: %w", err)
	}
	defer resp.Body.Close()

	remoteETag := resp.Header.Get("ETag")
	if remoteETag == "" {
		// If no ETag, download after interval
		return time.Since(lastDownloadTime) > downloadInterval, nil
	}

	// Compare with local file hash
	localHash, err := getFileHash(localBinaryPath)
	if err != nil {
		return true, nil
	}

	return localHash != remoteETag, nil
}

func getFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func checkSoftwareUpdate(ctx context.Context) error {
	needsDownload, err := shouldDownloadNewBinary(ctx)
	if err != nil {
		return fmt.Errorf("error checking binary status: %w", err)
	}

	if needsDownload {
		downloadCmd := exec.CommandContext(ctx, "curl", "-sSL", "-o", localBinaryPath, binaryURL)
		if err := downloadCmd.Run(); err != nil {
			return fmt.Errorf("error downloading latest binary: %w", err)
		}

		if err := os.Chmod(localBinaryPath, 0755); err != nil {
			return fmt.Errorf("error changing permissions of latest binary: %w", err)
		}

		lastDownloadTime = time.Now()
		logger.Info("Downloaded new binary version")
	}

	// Get version of the latest binary
	versionCmd := exec.CommandContext(ctx, localBinaryPath, "--version")
	var out bytes.Buffer
	versionCmd.Stdout = &out

	if err := versionCmd.Run(); err != nil {
		return fmt.Errorf("error running latest binary version command: %w", err)
	}

	latestVersionOutput := out.String()
	parts := strings.Split(latestVersionOutput, "|")
	if len(parts) >= 3 {
		commitLine := parts[0]
		latestCommitParts := strings.Split(commitLine, " ")
		if len(latestCommitParts) >= 2 {
			latestCommitHash := strings.TrimSpace(latestCommitParts[1])

			// Update OpenTelemetry metric
			if currentCommitHash == latestCommitHash {
				metrics.SetSoftwareUpToDate(true)
				logger.Info("Software is up to date")
			} else {
				metrics.SetSoftwareUpToDate(false)
				logger.Info("Software is NOT up to date. Current: %s, Latest: %s",
					currentCommitHash, latestCommitHash)
			}
			return nil
		}
	}

	return fmt.Errorf("unexpected latest version output format: %s", latestVersionOutput)
}
