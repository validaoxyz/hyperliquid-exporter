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
)

// statusStakeRows decodes the status stream's current_stakes field across
// hl-node schema generations. Builds before 2026-07 wrote a bare array of
// rows; builds since wrap the rows as {"validator_to_stake": [...]}. The
// row element types also changed over time ([validator, signer] address
// pairs before, [validator, stake] after), so callers must type-check
// row[1] before using it.
func statusStakeRows(raw json.RawMessage) [][]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var rows [][]interface{}
	if err := json.Unmarshal(raw, &rows); err == nil {
		return rows
	}
	var wrapped struct {
		ValidatorToStake [][]interface{} `json:"validator_to_stake"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		return wrapped.ValidatorToStake
	}
	return nil
}

// registerStakeRows registers whatever identity information the stake rows
// carry: validator addresses always, signer->validator mappings only on the
// legacy [validator, signer] shape. Returns the number of signer mappings
// registered and a local signer->validator map for immediate lookups.
func registerStakeRows(rows [][]interface{}) (int, map[string]string) {
	signerToValidator := make(map[string]string)
	count := 0
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		validatorAddr, _ := row[0].(string)
		if validatorAddr == "" {
			continue
		}
		metrics.RegisterFullAddress(strings.ToLower(validatorAddr))
		if signerAddr, ok := row[1].(string); ok && signerAddr != "" {
			signerToValidator[strings.ToLower(signerAddr)] = strings.ToLower(validatorAddr)
			metrics.RegisterSignerMapping(strings.ToLower(signerAddr), strings.ToLower(validatorAddr))
			count++
		}
	}
	return count, signerToValidator
}

// state tracking for reducing log spam
var (
	lastValidatorAddress string
	lastMappingSource    string // "local", "api", or ""
)

// jailedLocalPrev tracks the validators currently published on
// hl_consensus_validator_jailed_local so unjailed ones are removed.
// Only touched from the validator_status goroutine.
var jailedLocalPrev = map[string]bool{}

// publishJailedLocal reconciles the node-local jailed set gauge to the
// current status line. An empty list clears every series.
func publishJailedLocal(current []string) {
	seen := make(map[string]bool, len(current))
	for _, v := range current {
		v = strings.ToLower(v)
		if v == "" {
			continue
		}
		seen[v] = true
		metrics.HLConsensusValidatorJailedLocal.WithLabelValues(v).Set(1)
	}
	for v := range jailedLocalPrev {
		if !seen[v] {
			metrics.HLConsensusValidatorJailedLocal.DeleteLabelValues(v)
		}
	}
	jailedLocalPrev = seen
}

func StartValidatorStatusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	goSafe("validator_status", func() {
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
				metrics.MarkMonitorTick("validator_status")
			}
		}
	})
}

func readValidatorStatus(nodeHome string) error {
	statusDir := filepath.Join(nodeHome, "data/node_logs/status/hourly")

	// check if status directory exists first - if not, this isn't a validator node
	if _, err := os.Stat(statusDir); os.IsNotExist(err) {
		metrics.SetIsValidator(false)
		metrics.SetValidatorAddress("")
		return nil
	}

	latestFile, err := latestHourlyFile(statusDir)
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
		HomeValidator           string          `json:"home_validator"`
		Round                   int64           `json:"round"`
		CurrentStakes           json.RawMessage `json:"current_stakes"`
		CurrentJailedValidators []string        `json:"current_jailed_validators"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		return fmt.Errorf("failed to parse validator data: %w", err)
	}

	// register identity info from current_stakes; signer mappings only
	// exist on the legacy row shape, newer builds rely on the API mapping
	_, signerToValidator := registerStakeRows(statusStakeRows(data.CurrentStakes))

	// node-local jailed set (present on 2026+ builds; empty list clears)
	publishJailedLocal(data.CurrentJailedValidators)

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
	// status lines carry per-validator maps for the whole validator set and
	// regularly exceed bufio's default 64 KiB token limit
	scanner.Buffer(make([]byte, 1<<20), 8<<20)
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

	latestFile, err := latestHourlyFile(statusDir)
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
		CurrentStakes json.RawMessage `json:"current_stakes"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		logger.WarningComponent("consensus", "Failed to parse validator data: %v", err)
		return "", false
	}

	if data.HomeValidator == "" {
		return "", false
	}

	// legacy rows map our signer to the validator address directly
	homeSigner := strings.ToLower(data.HomeValidator)
	for _, row := range statusStakeRows(data.CurrentStakes) {
		if len(row) < 2 {
			continue
		}
		validatorAddr, _ := row[0].(string)
		signerAddr, ok := row[1].(string)
		if ok && strings.ToLower(signerAddr) == homeSigner && validatorAddr != "" {
			return validatorAddr, true
		}
	}

	// newer builds don't carry signer info here; try the API-fed mapping
	if validatorAddr, ok := metrics.GetValidatorForSigner(homeSigner); ok {
		return validatorAddr, true
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

	latestFile, err := latestHourlyFile(statusDir)
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
		CurrentStakes json.RawMessage `json:"current_stakes"`
	}

	if err := json.Unmarshal(rawData[1], &data); err != nil {
		logger.WarningComponent("consensus", "Failed to parse validator data for mapping population: %v", err)
		return nil
	}

	// populate signer->validator mappings; on the newer stake-row shape
	// there are none here and the API monitor covers it instead
	count, _ := registerStakeRows(statusStakeRows(data.CurrentStakes))
	logger.InfoComponent("consensus", "Pre-populated %d signer->validator mappings from local status file", count)
	return nil
}
