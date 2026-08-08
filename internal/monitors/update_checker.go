package monitors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const updateCheckInterval = 30 * time.Minute

var (
	cachedLatestHash   string
	cachedETag         string
	localVisorHash     string
	localVisorMtime    time.Time
	visorMissingLogged bool
)

// StartUpdateChecker compares the LOCAL hl-visor build against the latest
// hl-visor published on the Hyperliquid CDN and drives
// hl_software_up_to_date from that pair.
//
// hl-visor is the binary operators manage; it swaps its hl-node child on
// its own, so comparing the CDN visor against the local hl-node commit
// (what an earlier version did) flags "outdated" whenever the child has
// been auto-updated mid-release. The remote side is fetched with a
// conditional request: as long as the CDN object's ETag is unchanged we
// skip the download entirely.
func StartUpdateChecker(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceUpdate, true)
	goSafe("update_checker", func() {
		if err := checkSoftwareUpdate(ctx, cfg); err != nil {
			ReportError(ctx, "update_checker", errCh, fmt.Errorf("update checker error: %w", err))
		}
		metrics.MarkMonitorTick("update_checker")

		ticker := time.NewTicker(updateCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := checkSoftwareUpdate(ctx, cfg); err != nil {
					ReportError(ctx, "update_checker", errCh, fmt.Errorf("update checker error: %w", err))
				}
				metrics.MarkMonitorTick("update_checker")
			}
		}
	})
}

func checkSoftwareUpdate(ctx context.Context, cfg config.Config) error {
	visorPath := filepath.Join(cfg.BinaryHome, "hl-visor")
	info, err := os.Stat(visorPath)
	if err != nil {
		if !visorMissingLogged {
			logger.InfoComponent("system",
				"No hl-visor at %s; update check disabled (hl_software_up_to_date stays unset). Set BINARY_HOME if the visor lives elsewhere.", visorPath)
			visorMissingLogged = true
		}
		return nil
	}

	if localVisorHash == "" || info.ModTime().After(localVisorMtime) {
		commit, _, err := binaryVersionViaCopy(ctx, visorPath)
		if err != nil {
			return fmt.Errorf("local hl-visor version: %w", err)
		}
		localVisorHash = commit
		localVisorMtime = info.ModTime()
	}

	if err := refreshLatestVisorHash(ctx, cfg.Chain); err != nil {
		return err
	}

	updateUpToDateStatus()
	return nil
}

// refreshLatestVisorHash resolves the commit hash of the newest hl-visor on
// the CDN. A stored ETag turns the periodic check into a 304 round-trip;
// the full download+exec only happens when the CDN object actually changed.
func refreshLatestVisorHash(ctx context.Context, chain string) error {
	binaryURL, err := visorURLForChain(chain)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binaryURL, nil)
	if err != nil {
		return fmt.Errorf("error building update request: %w", err)
	}
	if cachedETag != "" && cachedLatestHash != "" {
		req.Header.Set("If-None-Match", cachedETag)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error fetching latest binary: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("unexpected status %s fetching %s", resp.Status, binaryURL)
	}

	tmpFile, err := os.CreateTemp("", "hl-visor-*.tmp")
	if err != nil {
		return fmt.Errorf("error creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("error downloading latest binary: %w", err)
	}
	tmpFile.Close()

	commit, _, err := binaryVersionDirect(ctx, tmpPath)
	if err != nil {
		return fmt.Errorf("latest hl-visor version: %w", err)
	}

	cachedLatestHash = commit
	cachedETag = resp.Header.Get("ETag")
	return nil
}

func visorURLForChain(raw string) (string, error) {
	chain, err := config.NormalizeChain(raw)
	if err != nil {
		return "", fmt.Errorf("invalid update-check chain: %w", err)
	}
	if chain == "mainnet" {
		return "https://binaries.hyperliquid.xyz/Mainnet/hl-visor", nil
	}
	return "https://binaries.hyperliquid-testnet.xyz/Testnet/hl-visor", nil
}

func updateUpToDateStatus() {
	if localVisorHash == "" || cachedLatestHash == "" {
		return
	}

	if localVisorHash == cachedLatestHash {
		metrics.SetSoftwareUpToDate(true)
	} else {
		metrics.SetSoftwareUpToDate(false)
		logger.InfoComponent("system", "hl-visor is NOT up to date. Local: %s, Latest: %s",
			localVisorHash, cachedLatestHash)
	}
}
