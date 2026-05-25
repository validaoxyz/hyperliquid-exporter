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
		logger.InfoComponent("latency", "Validator latency metrics not available (non-validator node) - skipping latency monitoring")
		return
	}

	logger.InfoComponent("latency", "Starting validator latency monitor")

	// start monitoring goroutines
	goSafe("validator_latency", func() { m.monitorLatencies(ctx, errCh) })
	goSafe("validator_latency", func() { m.monitorEMA(ctx, errCh) })
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

// processes all validator latency files.
//
// On hl-node versions shipped since mid-2025, each validator's directory
// contains `hourly/<YYYYMMDD>/<H>` files (numeric hour with no leading
// zero — same quirk as everywhere else). Older releases wrote a flat
// `<YYYYMMDD>` file at the top of the validator dir; we keep a fallback
// path for them.
func (m *ValidatorLatencyMonitor) processLatencyFiles() error {
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

		hourlyRoot := filepath.Join(m.latencyDir, validator, "hourly")
		var filePath string
		if _, err := os.Stat(hourlyRoot); err == nil {
			// nested-hourly layout — find the latest hour-file numerically.
			path, err := latestHourlyFile(hourlyRoot)
			if err != nil {
				continue
			}
			filePath = path
		} else {
			// legacy flat layout fallback.
			today := time.Now().Format("20060102")
			filePath = filepath.Join(m.latencyDir, validator, today)
		}

		if err := m.processValidatorLatencyFile(validator, filePath); err != nil {
			logger.DebugComponent("latency", "Error processing latency file for %s: %v", validator, err)
		}
	}

	return nil
}

// processes a single validator's latency file. On hl-node versions
// shipped since mid-2025, the layout under data/validator_latency/<addr>/
// is nested-hourly: hourly/<YYYYMMDD>/<H>, NOT a flat <YYYYMMDD> file
// like the older releases used. We attempt nested-hourly first and
// fall back to the legacy flat layout for backwards compatibility.
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

	// stat-based offset reset: if the file shrank (rotation) or this is
	// a new file we haven't tracked, restart at 0.
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	key := validator + "|" + filePath
	lastPos, exists := m.lastProcessed[key]
	if exists && lastPos > info.Size() {
		// file rotated under us — start over
		lastPos = 0
	}
	if exists && lastPos > 0 {
		if _, err := file.Seek(lastPos, 0); err != nil {
			return fmt.Errorf("failed to seek: %w", err)
		}
	}

	scanner := bufio.NewScanner(file)
	// raise the scanner buffer cap: hourly latency files can carry one long
	// line per round and the default 64 KiB token limit truncates them.
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
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

	pos, _ := file.Seek(0, 1)
	m.lastProcessed[key] = pos

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

	// The latest EMA line is a FULL SNAPSHOT of every validator at this
	// timestamp. Build a validator->ema map from it and reconcile the gauge
	// to that snapshot in one call below, rather than setting per entry. This
	// fixes stale frozen series: a validator that dropped out of the file (or
	// whose value became the no-data sentinel and is filtered here) is absent
	// from the snapshot and is therefore removed from the gauge. An all-
	// sentinel line yields an empty snapshot, which correctly clears the
	// series. We only reach this point after a successful parse, so replacing
	// the whole map is safe.
	snapshot := map[string]float64{}
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

		// hl-node seeds a validator's EMA with the no-data sentinel 0.4
		// (seconds) until it has observed real heartbeat-ack latencies.
		// Skip ONLY that exact sentinel — the previous `>= 0.4` filter
		// also discarded genuine high latencies (anything at/above 400ms),
		// which both froze this gauge near ~400ms on a slow network and
		// disagreed with the unfiltered raw-latency path above. Use a tiny
		// epsilon so floating-point representation of the literal 0.4 still
		// matches; real EMAs above the sentinel are now reported.
		const emaNoDataSentinel = 0.4
		if emaValue >= emaNoDataSentinel-1e-9 && emaValue <= emaNoDataSentinel+1e-9 {
			continue
		}

		snapshot[validator] = emaValue
	}

	metrics.ReplaceValidatorLatencyEMA(snapshot)

	return nil
}
