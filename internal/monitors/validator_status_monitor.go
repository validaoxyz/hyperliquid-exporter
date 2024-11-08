package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

func StartValidatorStatusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := readValidatorStatus(cfg.NodeHome)
				if err != nil {
					logger.Error("Validator Status Monitor error: %v", err)
					errCh <- err
				}
			}
		}
	}()
}

func readValidatorStatus(nodeHome string) error {
	statusDir := filepath.Join(nodeHome, "data/node_logs/status/hourly")
	latestFile, err := utils.GetLatestFile(statusDir)
	if err != nil {
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	fileInfo, err := os.Stat(latestFile)
	if err != nil {
		logger.Warning("Error getting status file info: %v", err)
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	if time.Since(fileInfo.ModTime()) > 24*time.Hour {
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	lastLine, err := ReadLastLine(latestFile)
	if err != nil {
		logger.Warning("Error reading last line of status file: %v", err)
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	if err := processValidatorStatusLine(lastLine); err != nil {
		logger.Warning("Error processing validator status line: %v", err)
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	return nil
}

func processValidatorStatusLine(line string) error {
	var statusData struct {
		ValidatorAddress string `json:"validator_address"`
	}

	err := json.Unmarshal([]byte(line), &statusData)
	if err != nil {
		return fmt.Errorf("failed to parse validator status line: %w", err)
	}

	// If we have a ValidatorAddress, assume the node is a validator
	if statusData.ValidatorAddress != "" {
		metrics.SetValidatorAddress(statusData.ValidatorAddress)
		metrics.SetIsValidator(true)
	} else {
		metrics.SetValidatorAddress("")
		metrics.SetIsValidator(false)
	}

	return nil
}

func ReadLastLine(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lastLine string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lastLine = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return lastLine, nil
}
