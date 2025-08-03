package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// state tracking for reducing log spam
var (
	lastValidatorAddress string
	lastMappingSource    string // "local", "api", or ""
)

func StartValidatorStatusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		// initial status check to set up logging context
		statusDir := filepath.Join(cfg.NodeHome, "data/node_logs/status/hourly")
		if _, err := os.Stat(statusDir); os.IsNotExist(err) {
			logger.InfoComponent("consensus", "Validator status directory not found - monitoring for non-validator node")
		} else {
			logger.InfoComponent("consensus", "Monitoring validator status files for changes")
		}

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := readValidatorStatus(cfg.NodeHome)
				if err != nil {
					logger.ErrorComponent("consensus", "Validator Status Monitor error: %v", err)
					errCh <- err
				}
			}
		}
	}()
}

func readValidatorStatus(nodeHome string) error {
	statusDir := filepath.Join(nodeHome, "data/node_logs/status/hourly")

	// check if status directory exists first - if not, this isn't a validator node
	if _, err := os.Stat(statusDir); os.IsNotExist(err) {
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	latestFile, err := utils.GetLatestFile(statusDir)
	if err != nil {
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	fileInfo, err := os.Stat(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error getting status file info: %v", err)
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	// if last file is > 12 hours, optimistically assume node is no longer a validator
	if time.Since(fileInfo.ModTime()) > 12*time.Hour {
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	lastLine, err := ReadLastLine(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error reading last line of status file: %v", err)
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	if err := processValidatorStatusLine(lastLine); err != nil {
		logger.WarningComponent("consensus", "Error processing validator status line: %v", err)
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

	// parse the second element (index 1) which contains the actual data
	var data struct {
		HomeValidator string          `json:"home_validator"`
		Round         int64           `json:"round"`
		CurrentStakes [][]interface{} `json:"current_stakes"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		return fmt.Errorf("failed to parse validator data: %w", err)
	}

	// first, build signer->validator mapping from current_stakes
	signerToValidator := make(map[string]string)
	for _, row := range data.CurrentStakes {
		if len(row) < 2 {
			continue
		}
		validatorAddr, _ := row[0].(string)
		signerAddr, _ := row[1].(string)
		if validatorAddr != "" && signerAddr != "" {
			signerToValidator[strings.ToLower(signerAddr)] = strings.ToLower(validatorAddr)
			metrics.RegisterSignerMapping(strings.ToLower(signerAddr), strings.ToLower(validatorAddr))

			// register the full validator address for expansion
			metrics.RegisterFullAddress(strings.ToLower(validatorAddr))
		}
	}

	// now handle home_validator (which is actually the signer address)
	if data.HomeValidator != "" {
		homeSigner := strings.ToLower(data.HomeValidator)

		// map signer to validator address
		// first try local mapping from current_stakes
		if validatorAddr, ok := signerToValidator[homeSigner]; ok {
			metrics.SetValidatorAddress(validatorAddr)
			metrics.SetIsValidator(true)

			// register the full address for expansion
			metrics.RegisterFullAddress(validatorAddr)

			// only log if this is a change from last state
			if validatorAddr != lastValidatorAddress || lastMappingSource != "local" {
				logger.InfoComponent("consensus", "Found validator address: %s (signer: %s)", validatorAddr, data.HomeValidator)
				lastValidatorAddress = validatorAddr
				lastMappingSource = "local"
			}
		} else if validatorAddr, ok := metrics.GetValidatorForSigner(homeSigner); ok {
			// fallback to global mapping from API
			metrics.SetValidatorAddress(validatorAddr)
			metrics.SetIsValidator(true)

			// register the full address for expansion
			metrics.RegisterFullAddress(validatorAddr)

			// only log if this is a change from last state
			if validatorAddr != lastValidatorAddress || lastMappingSource != "api" {
				logger.InfoComponent("consensus", "Found validator address from API: %s (signer: %s)", validatorAddr, data.HomeValidator)
				lastValidatorAddress = validatorAddr
				lastMappingSource = "api"
			}
		} else {
			// fallback: if mapping not found anywhere, use signer as validator
			// this shouldn't happen if API is working properly
			metrics.SetValidatorAddress(data.HomeValidator)
			metrics.SetIsValidator(true)

			// no mapping found - use signer as validator address silently
			// this can happen normally during startup before API mappings are populated
			lastValidatorAddress = data.HomeValidator
			lastMappingSource = ""
		}
	} else {
		metrics.SetValidatorAddress("")
		metrics.SetIsValidator(false)

		// only log if we previously had a validator address
		if lastValidatorAddress != "" {
			logger.InfoComponent("consensus", "No validator address found")
			lastValidatorAddress = ""
			lastMappingSource = ""
		}
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

	// check if status directory exists first - if not, this isn't a validator node
	if _, err := os.Stat(statusDir); os.IsNotExist(err) {
		return "", false
	}

	latestFile, err := utils.GetLatestFile(statusDir)
	if err != nil {
		// only log debug since missing status files are normal for non-validator nodes
		logger.DebugComponent("consensus", "Error finding latest status file: %v", err)
		return "", false
	}

	fileInfo, err := os.Stat(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error getting status file info: %v", err)
		return "", false
	}

	if time.Since(fileInfo.ModTime()) > 24*time.Hour {
		return "", false
	}

	lastLine, err := ReadLastLine(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error reading last line of status file: %v", err)
		return "", false
	}

	var rawData []json.RawMessage
	if err := json.Unmarshal([]byte(lastLine), &rawData); err != nil {
		logger.WarningComponent("consensus", "Failed to parse status array: %v", err)
		return "", false
	}

	if len(rawData) != 2 {
		logger.WarningComponent("consensus", "Unexpected validator status data format")
		return "", false
	}

	var data struct {
		HomeValidator string          `json:"home_validator"`
		CurrentStakes [][]interface{} `json:"current_stakes"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		logger.WarningComponent("consensus", "Failed to parse validator data: %v", err)
		return "", false
	}

	if data.HomeValidator == "" {
		return "", false
	}

	// build signer->validator mapping from current_stakes
	homeSigner := strings.ToLower(data.HomeValidator)
	for _, row := range data.CurrentStakes {
		if len(row) < 2 {
			continue
		}
		validatorAddr, _ := row[0].(string)
		signerAddr, _ := row[1].(string)
		if strings.ToLower(signerAddr) == homeSigner && validatorAddr != "" {
			// found the mapping for our signer
			return validatorAddr, true
		}
	}

	// if mapping not found, return signer as validator
	// this can happen normally during startup before mappings are populated
	return data.HomeValidator, true
}

// pre-populates all signer->validator mappings from the latest status file
// this should be called during initialization before any monitors start
func PopulateSignerMappings(nodeHome string) error {
	statusDir := filepath.Join(nodeHome, "data/node_logs/status/hourly")

	// check if status directory exists
	if _, err := os.Stat(statusDir); os.IsNotExist(err) {
		logger.InfoComponent("consensus", "Status directory not found - not a validator node, will populate mappings from API")
		// for non-validator nodes we'll rely on the validator API monitor to populate mappings
		// the API monitor runs immediately on startup now
		return nil
	}

	latestFile, err := utils.GetLatestFile(statusDir)
	if err != nil {
		logger.WarningComponent("consensus", "Error finding latest status file for mapping population: %v", err)
		return nil // non-fatal, monitors will populate mappings later
	}

	lastLine, err := ReadLastLine(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error reading status file for mapping population: %v", err)
		return nil
	}

	var rawData []json.RawMessage
	if err := json.Unmarshal([]byte(lastLine), &rawData); err != nil {
		logger.WarningComponent("consensus", "Failed to parse status data for mapping population: %v", err)
		return nil
	}

	if len(rawData) != 2 {
		return nil
	}

	var data struct {
		CurrentStakes [][]interface{} `json:"current_stakes"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		logger.WarningComponent("consensus", "Failed to parse validator data for mapping population: %v", err)
		return nil
	}

	// populate all signer->validator mappings
	count := 0
	for _, row := range data.CurrentStakes {
		if len(row) < 2 {
			continue
		}
		validatorAddr, _ := row[0].(string)
		signerAddr, _ := row[1].(string)
		if validatorAddr != "" && signerAddr != "" {
			metrics.RegisterSignerMapping(strings.ToLower(signerAddr), strings.ToLower(validatorAddr))

			// register the full validator address for expansion
			metrics.RegisterFullAddress(strings.ToLower(validatorAddr))
			count++
		}
	}

	logger.InfoComponent("consensus", "Pre-populated %d signer->validator mappings from local status file", count)
	return nil
}
