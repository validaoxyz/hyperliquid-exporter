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

type StatusData struct {
	Timestamp string `json:"0"`
	Data      struct {
		HomeValidator string          `json:"home_validator"`
		Round         int64           `json:"round"`
		CurrentStakes [][]interface{} `json:"current_stakes"`
	} `json:"1"`
}

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
	var rawData []json.RawMessage
	if err := json.Unmarshal([]byte(line), &rawData); err != nil {
		return fmt.Errorf("failed to parse status array: %w", err)
	}

	if len(rawData) != 2 {
		return fmt.Errorf("unexpected validator status data format")
	}

	// Parse the second element (index 1) which contains the actual data
	var data struct {
		HomeValidator string          `json:"home_validator"`
		Round         int64           `json:"round"`
		CurrentStakes [][]interface{} `json:"current_stakes"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		return fmt.Errorf("failed to parse validator data: %w", err)
	}

	if data.HomeValidator != "" {
		metrics.SetValidatorAddress(data.HomeValidator)
		metrics.SetIsValidator(true)
		logger.Info("Found validator address: %s", data.HomeValidator)
	} else {
		metrics.SetValidatorAddress("")
		metrics.SetIsValidator(false)
		logger.Info("No validator address found")
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

func GetValidatorStatus(nodeHome string) (string, bool) {
	statusDir := filepath.Join(nodeHome, "data/node_logs/status/hourly")
	latestFile, err := utils.GetLatestFile(statusDir)
	if err != nil {
		logger.Warning("Error finding latest status file: %v", err)
		return "", false
	}

	fileInfo, err := os.Stat(latestFile)
	if err != nil {
		logger.Warning("Error getting status file info: %v", err)
		return "", false
	}

	if time.Since(fileInfo.ModTime()) > 24*time.Hour {
		return "", false
	}

	lastLine, err := ReadLastLine(latestFile)
	if err != nil {
		logger.Warning("Error reading last line of status file: %v", err)
		return "", false
	}

	var rawData []json.RawMessage
	if err := json.Unmarshal([]byte(lastLine), &rawData); err != nil {
		logger.Warning("Failed to parse status array: %v", err)
		return "", false
	}

	if len(rawData) != 2 {
		logger.Warning("Unexpected validator status data format")
		return "", false
	}

	var data struct {
		HomeValidator string `json:"home_validator"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		logger.Warning("Failed to parse validator data: %v", err)
		return "", false
	}

	return data.HomeValidator, data.HomeValidator != ""
}
