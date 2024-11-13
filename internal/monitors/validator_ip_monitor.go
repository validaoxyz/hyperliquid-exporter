package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

var (
	validatorIPMutex  sync.RWMutex
	validatorMonikers = make(map[string]string)
	validatorIPs      = make(map[string]string)
	validatorStakes   = make(map[string]float64)
)

type validatorWithStake struct {
	address string
	stake   float64
}

func StartValidatorIPMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		stateDir := filepath.Join(cfg.NodeHome, "data/periodic_abci_states")

		// Add directory check
		if _, err := os.Stat(stateDir); os.IsNotExist(err) {
			logger.Error("State directory does not exist: %s", stateDir)
			errCh <- fmt.Errorf("state directory does not exist: %s", stateDir)
			return
		}

		var currentFile string
		var lastAttempt time.Time

		// Start the ping monitoring in a separate goroutine
		go monitorValidatorRTT(ctx, errCh)

		// Initial attempt
		if err := processLatestState(ctx, stateDir, &currentFile, cfg.NodeBinary, cfg.Chain); err != nil {
			logger.Error("Initial state processing failed: %v", err)
			errCh <- err
		}
		lastAttempt = time.Now()

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Only retry if it's been at least an hour since last attempt
				if time.Since(lastAttempt) < time.Hour {
					continue
				}

				if err := processLatestState(ctx, stateDir, &currentFile, cfg.NodeBinary, cfg.Chain); err != nil {
					logger.Error("State processing failed: %v", err)
					errCh <- err
				}
				lastAttempt = time.Now()
			}
		}
	}()
}

func processLatestState(ctx context.Context, stateDir string, currentFile *string, nodeBinary string, chain string) error {
	latestFile, err := utils.GetLatestFile(stateDir)
	if err != nil {
		logger.Error("Error finding latest state file in dir %s: %v", stateDir, err)
		return fmt.Errorf("error finding latest state file: %w", err)
	}

	logger.Info("Processing state file: %s", latestFile)

	if latestFile == *currentFile {
		return nil
	}

	// Translate ABCI state file using node binary
	outputFile := "/tmp/latest_state.json"
	chainArg := strings.Title(strings.ToLower(chain))
	cmd := exec.Command(nodeBinary, "--chain", chainArg, "translate-abci-state", latestFile, outputFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Error("Error translating ABCI state: %v, stderr: %s", err, stderr.String())
		return fmt.Errorf("error translating ABCI state: %w", err)
	}
	defer os.Remove(outputFile)

	file, err := os.Open(outputFile)
	if err != nil {
		logger.Error("Error opening translated file: %v", err)
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	depth := 0
	inValidatorToProfile := false

	// Massive file so read tokens (until EOF or error)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error("Error reading token: %v", err)
			return err
		}

		switch t := token.(type) {
		case json.Delim:
			if t == '{' || t == '[' {
				depth++
			} else if t == '}' || t == ']' {
				depth--
			}
		case string:
			if t == "validator_to_profile" {
				inValidatorToProfile = true
				// Skip opening array delimiter
				decoder.Token()

				validatorIPMutex.Lock()
				// For each val
				for decoder.More() {
					var entry [2]interface{}
					if err := decoder.Decode(&entry); err != nil {
						logger.Error("Error decoding validator entry: %v", err)
						continue
					}

					validatorAddr, ok := entry[0].(string)
					if !ok {
						continue
					}

					profileMap, ok := entry[1].(map[string]interface{})
					if !ok {
						continue
					}

					nodeIP, ok := profileMap["node_ip"].(map[string]interface{})
					if !ok {
						continue
					}

					ip, ok := nodeIP["Ip"].(string)
					if !ok {
						continue
					}

					name, ok := profileMap["name"].(string)
					if !ok {
						continue
					}

					validatorIPs[validatorAddr] = ip
					validatorMonikers[validatorAddr] = name
					logger.Debug("Found validator: %s, IP: %s, Name: %s", validatorAddr, ip, name)
				}
				validatorIPMutex.Unlock()
				break
			}
		}
	}

	if !inValidatorToProfile {
		logger.Warning("validator_to_profile field not found in state file")
		return nil
	}

	logger.Info("Updated validator maps - IPs: %d, Monikers: %d", len(validatorIPs), len(validatorMonikers))
	*currentFile = latestFile
	return nil
}

func monitorValidatorRTT(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, validator := range getTopValidators(50) {
				validatorIPMutex.RLock()
				ip := validatorIPs[validator]
				validatorIPMutex.RUnlock()
				go measureRTT(ctx, validator, ip)
			}
		}
	}
}

func measureRTT(ctx context.Context, validator, ip string) {
	if ip == "" {
		return
	}

	moniker := ""
	validatorIPMutex.RLock()
	if m, ok := validatorMonikers[validator]; ok {
		moniker = m
	}
	validatorIPMutex.RUnlock()

	success := false
	var latency float64

	for port := 4000; port <= 4010; port++ {
		start := time.Now()
		address := fmt.Sprintf("%s:%d", ip, port)

		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			latency = float64(time.Since(start).Microseconds())
			success = true
			conn.Close()
			break
		}
	}

	if success && latency > 0 && validator != "" && moniker != "" && ip != "" {
		metrics.SetValidatorRTT(validator, moniker, ip, latency)
	}
}

func UpdateValidatorStake(validator string, stake float64) {
	validatorIPMutex.Lock()
	validatorStakes[validator] = stake
	validatorIPMutex.Unlock()
}

func getTopValidators(n int) []string {
	validatorIPMutex.RLock()
	defer validatorIPMutex.RUnlock()

	stakes := metrics.GetValidatorStakes()
	validators := make([]validatorWithStake, 0, len(stakes))

	// Only include validators that have IPs
	for addr, stake := range stakes {
		if ip, exists := validatorIPs[addr]; exists && ip != "" {
			validators = append(validators, validatorWithStake{addr, stake})
		}
	}

	// Sort by stake in descending order
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].stake > validators[j].stake
	})

	// Get top N validators
	count := min(n, len(validators))
	topValidators := make([]string, count)
	for i := 0; i < count; i++ {
		topValidators[i] = validators[i].address
	}

	return topValidators
}
