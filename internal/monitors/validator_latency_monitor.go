package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	config      *config.Config
	latencyDir  string
	emaDir      string
	rawState    map[string]*rawLatencyState // per-validator raw-file streaming state
	lastEMATime time.Time
}

// rawLatencyState tracks one validator's raw latency stream: read position
// plus the newest parsed sample, so each tick can publish a full reconciled
// snapshot (validators with stale files drop out instead of freezing).
type rawLatencyState struct {
	path     string
	offset   int64
	latency  float64
	round    int64
	hasData  bool
	fileTime time.Time
}

// rawLatencyStaleAfter is how old a validator's newest latency file may be
// before its raw series are withdrawn. Live peers update these files
// continuously; a stale file means the validator left the measured set.
const rawLatencyStaleAfter = 2 * time.Hour

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
		config:     cfg,
		latencyDir: filepath.Join(cfg.NodeHome, "data", "validator_latency"),
		emaDir:     filepath.Join(cfg.NodeHome, "data", "validator_latency_ema"),
		rawState:   make(map[string]*rawLatencyState),
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

	latencySnapshot := make(map[string]float64)
	roundSnapshot := make(map[string]int64)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		validator := entry.Name()
		if !strings.HasPrefix(validator, "0x") {
			continue
		}

		st := m.rawState[validator]
		if st == nil {
			st = &rawLatencyState{}
			m.rawState[validator] = st
		}

		hourlyRoot := filepath.Join(m.latencyDir, validator, "hourly")
		var filePath string
		if _, err := os.Stat(hourlyRoot); err == nil {
			// nested-hourly layout - find the latest hour-file numerically.
			path, err := latestHourlyFile(hourlyRoot)
			if err != nil {
				continue
			}
			filePath = path
		} else {
			// legacy flat layout fallback. hl-node writes date files in
			// UTC, so a host in another timezone must not pick "today"
			// from local time.
			today := time.Now().UTC().Format("20060102")
			filePath = filepath.Join(m.latencyDir, validator, today)
		}

		if err := m.readValidatorLatency(st, filePath); err != nil {
			logger.DebugComponent("latency", "Error processing latency file for %s: %v", validator, err)
		}

		// publish only validators whose files are still being written;
		// a stale file belongs to a validator that left the measured set
		if st.hasData && time.Since(st.fileTime) <= rawLatencyStaleAfter {
			latencySnapshot[validator] = st.latency
			roundSnapshot[validator] = st.round
		}
	}

	// drop streaming state for validators gone long enough that keeping
	// their offsets serves nothing
	for v, st := range m.rawState {
		if st.hasData && time.Since(st.fileTime) > 24*time.Hour {
			delete(m.rawState, v)
		}
	}

	metrics.ReplaceValidatorLatency(latencySnapshot)
	metrics.ReplaceValidatorLatencyRound(roundSnapshot)

	return nil
}

// readValidatorLatency drains new complete lines from filePath and keeps
// the newest parsed sample in st. Offsets are tracked per validator and
// reset when the stream rotates to a new file.
func (m *ValidatorLatencyMonitor) readValidatorLatency(st *rawLatencyState, filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// file doesn't exist yet for today, skip
			return nil
		}
		return fmt.Errorf("stat file: %w", err)
	}
	st.fileTime = info.ModTime()

	if filePath != st.path {
		st.path, st.offset = filePath, 0
	} else if st.offset > info.Size() {
		// file rotated or was truncated under us - start over
		st.offset = 0
	}

	if st.offset == info.Size() {
		return nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	if st.offset > 0 {
		if _, err := file.Seek(st.offset, io.SeekStart); err != nil {
			return fmt.Errorf("failed to seek: %w", err)
		}
	}

	scanner := bufio.NewScanner(file)
	// raise the scanner buffer cap: hourly latency files can carry one long
	// line per round and the default 64 KiB token limit truncates them.
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	var latest *LatencyEntry

	for scanner.Scan() {
		var entry LatencyEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			logger.DebugComponent("latency", "Failed to parse latency entry: %v", err)
			continue
		}
		latest = &entry
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	pos, _ := file.Seek(0, io.SeekCurrent)
	st.offset = pos

	if latest != nil {
		st.latency, st.round, st.hasData = latest.Latency, latest.Round, true
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
	// EMA date files are written in UTC; never derive "today" from the
	// host's local timezone
	today := time.Now().UTC().Format("20060102")
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
