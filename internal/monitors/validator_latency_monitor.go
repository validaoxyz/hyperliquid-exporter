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

const (
	latencyPollInterval = 10 * time.Second
)

// validator latency monitor
type ValidatorLatencyMonitor struct {
	config        *config.Config
	latencyDir    string
	emaDir        string
	lastProcessed map[string]int64 // Track last processed position per validator
	lastEMATime   time.Time
}

// latency entry
type LatencyEntry struct {
	Time    string  `json:"time"`
	Round   int64   `json:"round"`
	Latency float64 `json:"latency"`
}

// EMAEntry represents the exponential moving average data
type EMAEntry struct {
	Time      string          `json:"time"`
	Latencies [][]interface{} `json:"latencies"`
}

// new validator latency monitor
func NewValidatorLatencyMonitor(cfg *config.Config) *ValidatorLatencyMonitor {
	return &ValidatorLatencyMonitor{
		config:        cfg,
		latencyDir:    filepath.Join(cfg.NodeHome, "data", "validator_latency"),
		emaDir:        filepath.Join(cfg.NodeHome, "data", "validator_latency_ema"),
		lastProcessed: make(map[string]int64),
	}
}

// starts the validator latency monitor
func StartValidatorLatencyMonitor(ctx context.Context, cfg *config.Config, errCh chan<- error) {
	m := NewValidatorLatencyMonitor(cfg)

	// check if latency directories exist
	if _, err := os.Stat(m.latencyDir); os.IsNotExist(err) {
		logger.InfoComponent("latency", "Validator latency directory not found, monitoring disabled: %s", m.latencyDir)
		return
	}

	logger.InfoComponent("latency", "Starting validator latency monitor")

	// start monitoring goroutines
	go m.monitorLatencies(ctx, errCh)
	go m.monitorEMA(ctx, errCh)
}

// monitors individual validator latency files
func (m *ValidatorLatencyMonitor) monitorLatencies(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(latencyPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.InfoComponent("latency", "Validator latency monitor shutting down")
			return
		case <-ticker.C:
			if err := m.processLatencyFiles(); err != nil {
				logger.ErrorComponent("latency", "Error processing latency files: %v", err)
				select {
				case errCh <- fmt.Errorf("validator latency monitor: %w", err):
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processes all validator latency files
func (m *ValidatorLatencyMonitor) processLatencyFiles() error {
	// list all validator directories
	entries, err := os.ReadDir(m.latencyDir)
	if err != nil {
		return fmt.Errorf("failed to read latency directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		validator := entry.Name()
		if !strings.HasPrefix(validator, "0x") {
			continue
		}

		// process today's file
		today := time.Now().Format("20060102")
		filePath := filepath.Join(m.latencyDir, validator, today)

		if err := m.processValidatorLatencyFile(validator, filePath); err != nil {
			logger.DebugComponent("latency", "Error processing latency file for %s: %v", validator, err)
			// continue with other validators
		}
	}

	return nil
}

// processes a single validator's latency file
func (m *ValidatorLatencyMonitor) processValidatorLatencyFile(validator, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// file doesn't exist yet for today, skip
			return nil
		}
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// get last processed position
	lastPos, exists := m.lastProcessed[validator]
	if exists && lastPos > 0 {
		// seek to last position
		if _, err := file.Seek(lastPos, 0); err != nil {
			return fmt.Errorf("failed to seek: %w", err)
		}
		logger.DebugComponent("latency", "Continuing from position %d for validator %s", lastPos, validator)
	} else {
		logger.DebugComponent("latency", "First run: starting from beginning of file for validator %s", validator)
	}

	scanner := bufio.NewScanner(file)
	var latestEntry *LatencyEntry

	for scanner.Scan() {
		var entry LatencyEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			logger.DebugComponent("latency", "Failed to parse latency entry: %v", err)
			continue
		}

		latestEntry = &entry
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	// update last processed position
	pos, _ := file.Seek(0, 1) // get current position
	m.lastProcessed[validator] = pos

	// update metrics with latest entry
	if latestEntry != nil {
		metrics.SetValidatorLatency(validator, latestEntry.Latency)
		metrics.SetValidatorLatencyRound(validator, latestEntry.Round)
	}

	return nil
}

// monitors the exponential moving average file
func (m *ValidatorLatencyMonitor) monitorEMA(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(latencyPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.processEMAFile(); err != nil {
				logger.ErrorComponent("latency", "Error processing EMA file: %v", err)
				select {
				case errCh <- fmt.Errorf("validator EMA monitor: %w", err):
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processes the exponential moving average file
func (m *ValidatorLatencyMonitor) processEMAFile() error {
	today := time.Now().Format("20060102")
	filePath := filepath.Join(m.emaDir, today)

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open EMA file: %w", err)
	}
	defer file.Close()

	// read the last line (most recent data)
	var lastLine string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lastLine = scanner.Text()
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	if lastLine == "" {
		return nil
	}

	// parse the JSON array
	var data []json.RawMessage
	if err := json.Unmarshal([]byte(lastLine), &data); err != nil {
		return fmt.Errorf("failed to parse EMA line: %w", err)
	}

	if len(data) != 2 {
		return fmt.Errorf("unexpected EMA format")
	}

	// parse timestamp
	var timestamp string
	if err := json.Unmarshal(data[0], &timestamp); err != nil {
		return fmt.Errorf("failed to parse timestamp: %w", err)
	}

	// parse timestamp - format doesn't include timezone
	entryTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
	if err != nil {
		return fmt.Errorf("failed to parse time: %w", err)
	}

	// only process if newer than last processed
	if !entryTime.After(m.lastEMATime) {
		return nil
	}
	m.lastEMATime = entryTime

	// parse latencies array
	var latencies [][]interface{}
	if err := json.Unmarshal(data[1], &latencies); err != nil {
		return fmt.Errorf("failed to parse latencies: %w", err)
	}

	// update metrics
	for _, entry := range latencies {
		if len(entry) != 2 {
			continue
		}

		validator, ok := entry[0].(string)
		if !ok {
			continue
		}

		emaValue, ok := entry[1].(float64)
		if !ok {
			continue
		}

		// skip validators with default 0.4 value (indicates no data)
		if emaValue >= 0.4 {
			continue
		}

		metrics.SetValidatorLatencyEMA(validator, emaValue)
	}

	return nil
}
