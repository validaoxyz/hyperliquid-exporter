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
	rows, err := decodeStatusStakeRows(raw)
	if err != nil {
		return nil
	}
	return rows
}

// decodeStatusStakeRows preserves the two supported schema generations while
// rejecting a null/malformed generation instead of silently treating it as a
// complete empty stake snapshot.
func decodeStatusStakeRows(raw json.RawMessage) ([][]interface{}, error) {
	var rawRows []json.RawMessage
	if err := unmarshalRequiredJSON(raw, &rawRows); err != nil {
		var wrapped struct {
			ValidatorToStake json.RawMessage `json:"validator_to_stake"`
		}
		if objectErr := unmarshalRequiredJSON(raw, &wrapped); objectErr != nil {
			return nil, fmt.Errorf("invalid current_stakes: %w", err)
		}
		if rowsErr := unmarshalRequiredJSON(wrapped.ValidatorToStake, &rawRows); rowsErr != nil {
			return nil, fmt.Errorf("invalid validator_to_stake: %w", rowsErr)
		}
	}

	rows := make([][]interface{}, 0, len(rawRows))
	for i, rawRow := range rawRows {
		var pair []json.RawMessage
		if err := unmarshalRequiredJSON(rawRow, &pair); err != nil || len(pair) != 2 {
			return nil, fmt.Errorf("invalid current_stakes row %d", i)
		}
		var identity string
		if err := unmarshalRequiredJSON(pair[0], &identity); err != nil || strings.TrimSpace(identity) == "" {
			return nil, fmt.Errorf("invalid current_stakes identity at row %d", i)
		}
		var second interface{}
		if err := unmarshalRequiredJSON(pair[1], &second); err != nil {
			return nil, fmt.Errorf("invalid current_stakes value at row %d", i)
		}
		switch value := second.(type) {
		case string:
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("empty current_stakes signer at row %d", i)
			}
		case float64:
			// Modern status rows carry a JSON number. Units remain unresolved;
			// shape validation deliberately does not reinterpret the value.
		default:
			return nil, fmt.Errorf("unsupported current_stakes value at row %d", i)
		}
		rows = append(rows, []interface{}{identity, second})
	}
	return rows, nil
}

// registerStakeRows extracts whatever identity information the stake rows
// carry: validator addresses always, signer->validator mappings only on the
// legacy [validator, signer] shape. It deliberately does not mutate the
// canonical registry; the startup population path installs the complete local
// generation once, and the API owns all later replacements.
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

type parsedValidatorStatus struct {
	SourceTime           time.Time
	HomeValidator        string
	Round                int64
	StakeRows            [][]interface{}
	JailedFieldPresent   bool
	CurrentJailedSigners []string
}

func parseValidatorStatusLine(line string) (parsedValidatorStatus, error) {
	var rawData []json.RawMessage
	if err := json.Unmarshal([]byte(line), &rawData); err != nil {
		return parsedValidatorStatus{}, fmt.Errorf("failed to parse status array: %w", err)
	}
	if len(rawData) != 2 {
		return parsedValidatorStatus{}, fmt.Errorf("unexpected validator status data format")
	}

	var timestamp string
	if err := unmarshalRequiredJSON(rawData[0], &timestamp); err != nil {
		return parsedValidatorStatus{}, fmt.Errorf("invalid validator status timestamp: %w", err)
	}
	sourceTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
	if err != nil {
		return parsedValidatorStatus{}, fmt.Errorf("invalid validator status timestamp: %w", err)
	}

	var body map[string]json.RawMessage
	if err := unmarshalRequiredJSON(rawData[1], &body); err != nil || body == nil {
		return parsedValidatorStatus{}, fmt.Errorf("invalid validator status body")
	}

	var parsed parsedValidatorStatus
	parsed.SourceTime = sourceTime
	if err := unmarshalRequiredJSON(body["home_validator"], &parsed.HomeValidator); err != nil {
		return parsedValidatorStatus{}, fmt.Errorf("invalid home_validator: %w", err)
	}
	if err := unmarshalRequiredJSON(body["round"], &parsed.Round); err != nil || parsed.Round < 0 {
		return parsedValidatorStatus{}, fmt.Errorf("invalid validator status round")
	}
	parsed.StakeRows, err = decodeStatusStakeRows(body["current_stakes"])
	if err != nil {
		return parsedValidatorStatus{}, err
	}
	if rawJailed, present := body["current_jailed_validators"]; present {
		parsed.JailedFieldPresent = true
		var signers []string
		if err := unmarshalRequiredJSON(rawJailed, &signers); err != nil {
			return parsedValidatorStatus{}, fmt.Errorf("invalid current_jailed_validators: %w", err)
		}
		parsed.CurrentJailedSigners, err = validateStatusSignerSet(signers)
		if err != nil {
			return parsedValidatorStatus{}, err
		}
	}
	return parsed, nil
}

// jailedLocalPrev tracks the signer-labelled rows currently published on
// hl_consensus_validator_jailed_local so unjailed ones are removed.
// Only touched from the validator_status goroutine.
var jailedLocalPrev = map[string][3]string{}

// publishJailedLocal reconciles the node-local jailed set gauge to the
// current status line. An empty list clears every series.
func publishJailedLocal(current []string) {
	identities := metrics.ResolveSignerSnapshot(current)
	seen := make(map[string][3]string, len(current))
	for _, signer := range current {
		signer = strings.ToLower(strings.TrimSpace(signer))
		if signer == "" {
			continue
		}
		identity := identities[signer]
		labels := [3]string{identity.Validator, identity.Signer, identity.Name}
		seen[signer] = labels
		metrics.HLConsensusValidatorJailedLocal.WithLabelValues(labels[0], labels[1], labels[2]).Set(1)
	}
	for signer, previous := range jailedLocalPrev {
		currentLabels, exists := seen[signer]
		if !exists || currentLabels != previous {
			metrics.HLConsensusValidatorJailedLocal.DeleteLabelValues(previous[0], previous[1], previous[2])
		}
	}
	jailedLocalPrev = seen
}

func StartValidatorStatusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceValidatorStatus, true)
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
					ReportError(ctx, "validator_status", errCh, err)
				}
			}
		}
	})
}

func readValidatorStatus(nodeHome string) error {
	statusDir := filepath.Join(nodeHome, "data/node_logs/status/hourly")
	metrics.MarkMonitorAttempt("validator_status")
	metrics.MarkSourceAttempt(metrics.SourceValidatorStatus)

	// check if status directory exists first - if not, this isn't a validator node
	if _, err := os.Stat(statusDir); err != nil {
		if os.IsNotExist(err) {
			metrics.MarkSourceAbsent(metrics.SourceValidatorStatus)
			metrics.SetIsValidator(false)
			return nil
		}
		metrics.MarkSourceError(metrics.SourceValidatorStatus, metrics.SourceFailureStat)
		return fmt.Errorf("stat validator status directory: %w", err)
	}
	latestFile, err := latestHourlyFile(statusDir)
	if err != nil {
		if os.IsNotExist(err) {
			metrics.MarkSourceAbsent(metrics.SourceValidatorStatus)
			metrics.SetIsValidator(false)
			return nil
		}
		metrics.MarkSourceError(metrics.SourceValidatorStatus, metrics.SourceFailureDiscovery)
		return fmt.Errorf("find latest validator status: %w", err)
	}

	fileInfo, err := os.Stat(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error getting status file info: %v", err)
		metrics.MarkSourceError(metrics.SourceValidatorStatus, metrics.SourceFailureStat)
		return fmt.Errorf("stat validator status file: %w", err)
	}

	// if last file is > 12 hours, optimistically assume node is no longer a validator
	if time.Since(fileInfo.ModTime()) > 12*time.Hour {
		metrics.SetIsValidator(false)
		return nil
	}

	lastLine, err := ReadLastLine(latestFile)
	if err != nil {
		logger.WarningComponent("consensus", "Error reading last line of status file: %v", err)
		metrics.MarkSourceError(metrics.SourceValidatorStatus, metrics.SourceFailureRead)
		return fmt.Errorf("read validator status file: %w", err)
	}
	metrics.MarkSourceReadOutcome(metrics.SourceValidatorStatus, true)

	if err := processValidatorStatusLine(lastLine); err != nil {
		logger.WarningComponent("consensus", "Error processing validator status line: %v", err)
		metrics.MarkSourceError(metrics.SourceValidatorStatus, metrics.SourceFailureSchema)
		return err
	}

	metrics.MarkSourceValidObservation(metrics.SourceValidatorStatus, time.Time{})
	metrics.MarkSourcePublication(metrics.SourceValidatorStatus)
	metrics.MarkMonitorValidObservation("validator_status")
	metrics.MarkMonitorPublication("validator_status")
	return nil
}

func processValidatorStatusLine(line string) error {
	data, err := parseValidatorStatusLine(line)
	if err != nil {
		return err
	}

	// register identity info from current_stakes; signer mappings only
	// exist on the legacy row shape, newer builds rely on the API mapping
	_, signerToValidator := registerStakeRows(data.StakeRows)

	// Node-local jailed set is version-optional. An explicit empty list clears;
	// omission on an older schema does not impersonate an authoritative empty.
	if data.JailedFieldPresent {
		publishJailedLocal(data.CurrentJailedSigners)
	}

	// now handle home_validator (which is actually the signer address)
	if data.HomeValidator != "" {
		homeSigner := strings.ToLower(data.HomeValidator)

		// map signer to validator address
		// first try local mapping from current_stakes
		if validatorAddr, ok := signerToValidator[homeSigner]; ok {
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
			// The node is still a validator, but its signer is not a validator
			// address. Keep the configured/resource identity immutable and leave
			// the address explicitly unknown until a complete registry resolves it.
			metrics.SetIsValidator(true)

			lastValidatorAddress = ""
			lastMappingSource = ""
		}
	} else {
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

func validateStatusSignerSet(signers []string) ([]string, error) {
	if len(signers) > validatorSummaryLimit {
		return nil, fmt.Errorf("current_jailed_validators count %d exceeds limit %d", len(signers), validatorSummaryLimit)
	}
	out := make([]string, 0, len(signers))
	seen := make(map[string]struct{}, len(signers))
	for i, signer := range signers {
		signer = strings.ToLower(strings.TrimSpace(signer))
		if !isFullHexAddress(signer) && !metrics.IsAddressTruncated(signer) {
			return nil, fmt.Errorf("invalid jailed signer at row %d", i)
		}
		if _, duplicate := seen[signer]; duplicate {
			return nil, fmt.Errorf("duplicate jailed signer at row %d", i)
		}
		seen[signer] = struct{}{}
		out = append(out, signer)
	}
	return out, nil
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

	data, err := parseValidatorStatusLine(lastLine)
	if err != nil {
		logger.WarningComponent("consensus", "Failed to parse validator status: %v", err)
		return "", false
	}

	if data.HomeValidator == "" {
		return "", false
	}

	// legacy rows map our signer to the validator address directly
	homeSigner := strings.ToLower(data.HomeValidator)
	for _, row := range data.StakeRows {
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

	// The signer proves validator role but not validator-address identity.
	// Returning it as a validator address would permanently mislabel the OTel
	// resource, which is intentionally immutable after startup.
	return "", true
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

	data, err := parseValidatorStatusLine(lastLine)
	if err != nil {
		logger.WarningComponent("consensus", "Failed to parse validator data for mapping population: %v", err)
		return nil
	}

	// populate signer->validator mappings; on the newer stake-row shape
	// there are none here and the API monitor covers it instead
	count, mappings := registerStakeRows(data.StakeRows)
	metrics.ReplaceProvisionalSignerMappings(mappings)
	logger.InfoComponent("consensus", "Pre-populated %d signer->validator mappings from local status file", count)
	return nil
}
