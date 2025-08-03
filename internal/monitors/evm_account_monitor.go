package monitors

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/abci"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// monitors ABCI state files for EVM account count
func StartEVMAccountMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	if !cfg.EnableEVM {
		logger.InfoComponent("evm", "EVM monitoring disabled - skipping EVM account monitor")
		return
	}

	// create ABCI reader with 8MB buffer
	reader := abci.NewReader(8)

	// monitor live state file (preferred)
	stateFile := filepath.Join(cfg.NodeHome, "hyperliquid_data/abci_state.rmp")
	logger.InfoComponent("evm", "Starting EVM account monitor on %s", stateFile)

	var lastModTime time.Time

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// check if file exists
				info, err := os.Stat(stateFile)
				if err != nil {
					if os.IsNotExist(err) {
						// try fallback to periodic snapshots if live state doesn't exist
						if err := processPeriodicSnapshot(cfg, reader); err != nil {
							errCh <- fmt.Errorf("evm account monitor: %w", err)
						}
						continue
					}
					errCh <- fmt.Errorf("evm account monitor: stat file: %w", err)
					continue
				}

				// skip if file hasn't been modified
				if info.ModTime().Equal(lastModTime) {
					continue
				}

				if err := processStateForAccounts(stateFile, reader); err != nil {
					errCh <- fmt.Errorf("evm account monitor: %w", err)
					continue
				}

				lastModTime = info.ModTime()
			}
		}
	}()
}

// falls back to periodic snapshots if live state is unavailable
func processPeriodicSnapshot(cfg config.Config, reader *abci.Reader) error {
	stateDir := filepath.Join(cfg.NodeHome, "data/periodic_abci_states")

	// find the latest snapshot
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return fmt.Errorf("read state dir: %w", err)
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rmp") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = filepath.Join(stateDir, entry.Name())
		}
	}

	if latestFile == "" {
		return fmt.Errorf("no snapshot files found")
	}

	return processStateForAccounts(latestFile, reader)
}

func processStateForAccounts(path string, reader *abci.Reader) error {
	// use native ABCI reader to extract context and account count
	_, accountCount, err := reader.ReadContextWithAccounts(path)
	if err != nil {
		return fmt.Errorf("read ABCI state: %w", err)
	}

	if accountCount > 0 {
		metrics.SetEVMAccountCount(accountCount)
		logger.Debug("EVM account count: %d", accountCount)
	}

	return nil
}
