package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	rawPeaks    map[string]struct{}
	emaTail     fileTailState
	lastEMATime time.Time
}

// rawLatencyState tracks one validator's raw latency stream: read position
// plus the newest parsed sample, so each tick can publish a full reconciled
// snapshot (validators with stale files drop out instead of freezing).
type rawLatencyState struct {
	tail       fileTailState
	latency    float64
	round      int64
	hasData    bool
	sampleTime time.Time
}

type fileTailState struct {
	path     string
	info     os.FileInfo
	offset   int64
	fragment []byte
}

type rawLatencyReadResult struct {
	valid      int
	invalid    bool
	peak       float64
	hasPeak    bool
	latestTime time.Time
}

// rawLatencyStaleAfter is how old a validator's newest latency file may be
// before its raw series are withdrawn. Live peers update these files
// continuously; a stale file means the validator left the measured set.
const rawLatencyStaleAfter = 2 * time.Hour

const (
	validatorEMAStaleAfter     = 2 * time.Minute
	validatorEMABootstrapBytes = int64(4 << 20)
)

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
		rawPeaks:   make(map[string]struct{}),
	}
}

// starts the validator latency monitor
func StartValidatorLatencyMonitor(ctx context.Context, cfg *config.Config, errCh chan<- error) {
	m := NewValidatorLatencyMonitor(cfg)
	metrics.RegisterSource(metrics.SourceValidatorLatency, true)
	metrics.RegisterSource(metrics.SourceValidatorLatencyEMA, true)

	logger.InfoComponent("latency", "Starting validator latency monitor with late source discovery")

	goSafe("validator_latency", func() { m.monitorLatencies(ctx, errCh) })
	goSafe("validator_latency", func() { m.monitorEMA(ctx, errCh) })
}

// monitors individual validator latency files
func (m *ValidatorLatencyMonitor) monitorLatencies(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(latencyPollInterval)
	defer ticker.Stop()
	if err := m.processLatencyFiles(); err != nil {
		logger.DebugComponent("latency", "Initial raw latency poll: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoComponent("latency", "Validator latency monitor shutting down")
			return
		case <-ticker.C:
			if err := m.processLatencyFiles(); err != nil {
				logger.ErrorComponent("latency", "Error processing latency files: %v", err)
				ReportError(ctx, "validator_latency", errCh, fmt.Errorf("validator latency monitor: %w", err))
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
	return m.processLatencyFilesAt(time.Now())
}

func (m *ValidatorLatencyMonitor) processLatencyFilesAt(now time.Time) error {
	metrics.MarkMonitorAttempt("validator_latency")
	metrics.MarkSourceAttempt(metrics.SourceValidatorLatency)
	entries, err := os.ReadDir(m.latencyDir)
	if err != nil {
		if os.IsNotExist(err) {
			m.withdrawRawLatency()
			m.rawState = make(map[string]*rawLatencyState)
			metrics.MarkSourceAbsent(metrics.SourceValidatorLatency)
			metrics.MarkMonitorPublication("validator_latency")
			return nil
		} else {
			metrics.MarkSourceError(metrics.SourceValidatorLatency, metrics.SourceFailureRead)
		}
		return fmt.Errorf("failed to read latency directory: %w", err)
	}

	latencySnapshot := make(map[string]float64)
	roundSnapshot := make(map[string]int64)
	peakSnapshot := make(map[string]float64)
	currentValidators := make(map[string]struct{})
	var newest time.Time
	validRecords := 0
	invalid := false

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		if !strings.HasPrefix(dirName, "0x") {
			continue
		}
		validator := strings.ToLower(dirName)
		currentValidators[validator] = struct{}{}

		st := m.rawState[validator]
		if st == nil {
			st = &rawLatencyState{}
			m.rawState[validator] = st
		}

		hourlyRoot := filepath.Join(m.latencyDir, dirName, "hourly")
		var filePath string
		if _, err := os.Stat(hourlyRoot); err == nil {
			// nested-hourly layout - find the latest hour-file numerically.
			path, err := latestHourlyFile(hourlyRoot)
			if err != nil {
				if os.IsNotExist(err) {
					delete(m.rawState, validator)
					continue
				}
				metrics.MarkSourceError(metrics.SourceValidatorLatency, metrics.SourceFailureRead)
				return fmt.Errorf("discover latency file for %s: %w", validator, err)
			}
			filePath = path
		} else if os.IsNotExist(err) {
			// legacy flat layout fallback. hl-node writes date files in
			// UTC, so a host in another timezone must not pick "today"
			// from local time.
			today := now.UTC().Format("20060102")
			filePath = filepath.Join(m.latencyDir, dirName, today)
		} else {
			metrics.MarkSourceError(metrics.SourceValidatorLatency, metrics.SourceFailureStat)
			return fmt.Errorf("stat hourly root for %s: %w", validator, err)
		}

		if _, err := os.Stat(filePath); err != nil {
			if os.IsNotExist(err) {
				delete(m.rawState, validator)
				continue
			}
			metrics.MarkSourceError(metrics.SourceValidatorLatency, metrics.SourceFailureStat)
			return fmt.Errorf("stat latency file for %s: %w", validator, err)
		}
		result, err := m.readValidatorLatency(st, filePath)
		if err != nil {
			metrics.MarkSourceError(metrics.SourceValidatorLatency, metrics.SourceFailureRead)
			return fmt.Errorf("process latency file: %w", err)
		}
		validRecords += result.valid
		invalid = invalid || result.invalid
		if result.latestTime.After(newest) {
			newest = result.latestTime
		}

		// publish only validators whose files are still being written;
		// a stale file belongs to a validator that left the measured set
		if st.hasData && now.Sub(st.sampleTime) <= rawLatencyStaleAfter {
			latencySnapshot[validator] = st.latency
			roundSnapshot[validator] = st.round
			if result.hasPeak {
				peakSnapshot[validator] = result.peak
			}
		}
	}

	// Drop stream state for validators no longer present in the complete
	// directory snapshot, and bound stale offset retention.
	for v, st := range m.rawState {
		_, present := currentValidators[v]
		if !present || (st.hasData && now.Sub(st.sampleTime) > 24*time.Hour) {
			delete(m.rawState, v)
		}
	}

	metrics.ReplaceValidatorLatency(latencySnapshot)
	metrics.ReplaceValidatorLatencyRound(roundSnapshot)
	m.replaceRawLatencyPeaks(peakSnapshot)
	if validRecords > 0 {
		metrics.MarkSourceValidObservation(metrics.SourceValidatorLatency, newest)
		metrics.MarkMonitorValidObservation("validator_latency")
	} else if len(entries) == 0 {
		metrics.MarkSourceAvailable(metrics.SourceValidatorLatency)
	} else {
		metrics.MarkSourceReadOutcome(metrics.SourceValidatorLatency, true)
		metrics.MarkSourceSchemaOutcome(metrics.SourceValidatorLatency, true)
	}
	metrics.MarkSourcePublication(metrics.SourceValidatorLatency)
	metrics.MarkMonitorPublication("validator_latency")
	if invalid {
		metrics.MarkSourceError(metrics.SourceValidatorLatency, metrics.SourceFailureSchema)
	}

	return nil
}

// readValidatorLatency drains new complete lines from filePath and keeps
// the newest parsed sample in st. Offsets are tracked per validator and
// reset when the stream rotates to a new file.
func (m *ValidatorLatencyMonitor) readValidatorLatency(st *rawLatencyState, filePath string) (rawLatencyReadResult, error) {
	lines, err := readCompleteAppends(&st.tail, filePath, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return rawLatencyReadResult{}, nil
		}
		return rawLatencyReadResult{}, err
	}
	var result rawLatencyReadResult
	for _, line := range lines {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			result.invalid = true
			continue
		}
		var entry LatencyEntry
		if unmarshalRequiredJSON(raw["time"], &entry.Time) != nil || unmarshalRequiredJSON(raw["round"], &entry.Round) != nil || unmarshalRequiredJSON(raw["latency"], &entry.Latency) != nil ||
			entry.Round < 0 || math.IsNaN(entry.Latency) || math.IsInf(entry.Latency, 0) || entry.Latency < 0 {
			result.invalid = true
			continue
		}
		sampleTime, ok := parseVisorTime(entry.Time)
		if !ok {
			result.invalid = true
			continue
		}
		result.valid++
		if !result.hasPeak || entry.Latency > result.peak {
			result.peak, result.hasPeak = entry.Latency, true
		}
		if sampleTime.After(result.latestTime) {
			result.latestTime = sampleTime
		}
		if !st.hasData || !sampleTime.Before(st.sampleTime) {
			st.latency, st.round, st.hasData, st.sampleTime = entry.Latency, entry.Round, true, sampleTime
		}
	}
	return result, nil
}

func (m *ValidatorLatencyMonitor) replaceRawLatencyPeaks(snapshot map[string]float64) {
	for signer, peak := range snapshot {
		metrics.HLConsensusValidatorLatencyPollMax.WithLabelValues(signer).Set(peak)
	}
	for signer := range m.rawPeaks {
		if _, ok := snapshot[signer]; !ok {
			metrics.HLConsensusValidatorLatencyPollMax.DeleteLabelValues(signer)
		}
	}
	m.rawPeaks = make(map[string]struct{}, len(snapshot))
	for signer := range snapshot {
		m.rawPeaks[signer] = struct{}{}
	}
}

func (m *ValidatorLatencyMonitor) withdrawRawLatency() {
	metrics.ReplaceValidatorLatency(nil)
	metrics.ReplaceValidatorLatencyRound(nil)
	m.replaceRawLatencyPeaks(nil)
}

const validatorTailMaxRecordBytes = 8 << 20

// readCompleteAppends tails one file by stable identity and retains the final
// unterminated record. State advances only after a successful read. A new EMA
// day may bootstrap from a bounded tail; raw streams start from byte zero.
func readCompleteAppends(state *fileTailState, path string, bootstrapBytes int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	reset := state.path != path || state.info == nil || !os.SameFile(state.info, info) || state.offset > info.Size()
	offset := state.offset
	fragment := append([]byte(nil), state.fragment...)
	discardPrefix := false
	if reset {
		offset = 0
		fragment = nil
		if bootstrapBytes > 0 && info.Size() > bootstrapBytes {
			offset = info.Size() - bootstrapBytes
			discardPrefix = true
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	newBytes := make([]byte, info.Size()-offset)
	if _, err := io.ReadFull(f, newBytes); err != nil {
		return nil, err
	}
	if discardPrefix {
		idx := bytes.IndexByte(newBytes, '\n')
		if idx < 0 {
			return nil, fmt.Errorf("no record boundary in bounded tail")
		}
		newBytes = newBytes[idx+1:]
	}
	combined := append(fragment, newBytes...)
	lastNL := bytes.LastIndexByte(combined, '\n')
	var complete []byte
	if lastNL >= 0 {
		complete = combined[:lastNL]
		fragment = append([]byte(nil), combined[lastNL+1:]...)
	} else {
		fragment = append([]byte(nil), combined...)
	}
	if len(fragment) > validatorTailMaxRecordBytes {
		return nil, fmt.Errorf("unterminated record exceeds %d bytes", validatorTailMaxRecordBytes)
	}

	state.path = path
	state.info = info
	state.offset = info.Size()
	state.fragment = fragment
	if len(complete) == 0 {
		return nil, nil
	}
	rawLines := bytes.Split(complete, []byte("\n"))
	lines := make([][]byte, 0, len(rawLines))
	for _, raw := range rawLines {
		line := bytes.TrimSpace(raw)
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines, nil
}

// monitors the exponential moving average file
func (m *ValidatorLatencyMonitor) monitorEMA(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(latencyPollInterval)
	defer ticker.Stop()
	if err := m.processEMAFile(); err != nil {
		logger.DebugComponent("latency", "Initial EMA poll: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.processEMAFile(); err != nil {
				logger.ErrorComponent("latency", "Error processing EMA file: %v", err)
				ReportError(ctx, "validator_latency", errCh, fmt.Errorf("validator EMA monitor: %w", err))
			}
		}
	}
}

// processes the exponential moving average file
func (m *ValidatorLatencyMonitor) processEMAFile() error {
	return m.processEMAFileAt(time.Now())
}

func (m *ValidatorLatencyMonitor) processEMAFileAt(now time.Time) error {
	metrics.MarkMonitorAttempt("validator_latency")
	metrics.MarkSourceAttempt(metrics.SourceValidatorLatencyEMA)
	// EMA date files are written in UTC; never derive "today" from the
	// host's local timezone
	today := now.UTC().Format("20060102")
	filePath := filepath.Join(m.emaDir, today)
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			m.withdrawEMA(true)
			metrics.MarkSourceAbsent(metrics.SourceValidatorLatencyEMA)
			metrics.MarkMonitorPublication("validator_latency")
			return nil
		}
		metrics.MarkSourceError(metrics.SourceValidatorLatencyEMA, metrics.SourceFailureStat)
		return fmt.Errorf("stat EMA file: %w", err)
	}
	if info.Size() == 0 {
		m.withdrawEMA(true)
		metrics.MarkSourceAvailable(metrics.SourceValidatorLatencyEMA)
		metrics.MarkSourcePublication(metrics.SourceValidatorLatencyEMA)
		metrics.MarkMonitorPublication("validator_latency")
		return nil
	}
	lines, err := readCompleteAppends(&m.emaTail, filePath, validatorEMABootstrapBytes)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceValidatorLatencyEMA, metrics.SourceFailureRead)
		return fmt.Errorf("tail EMA file: %w", err)
	}

	var (
		candidateTime     time.Time
		candidateState    string
		candidateSnapshot map[string]float64
		invalid           bool
	)
	for _, line := range lines {
		entryTime, state, snapshot, err := parseValidatorEMASnapshot(line)
		if err != nil {
			invalid = true
			continue
		}
		if entryTime.After(m.lastEMATime) && entryTime.After(candidateTime) {
			candidateTime, candidateState, candidateSnapshot = entryTime, state, snapshot
		}
	}
	if candidateTime.IsZero() {
		if !m.lastEMATime.IsZero() && now.Sub(m.lastEMATime) > validatorEMAStaleAfter {
			m.withdrawEMA(false)
			metrics.MarkSourcePublication(metrics.SourceValidatorLatencyEMA)
			metrics.MarkMonitorPublication("validator_latency")
		} else if m.lastEMATime.IsZero() && len(m.emaTail.fragment) == 0 {
			metrics.MarkSourceAvailable(metrics.SourceValidatorLatencyEMA)
		}
		if invalid {
			metrics.MarkSourceError(metrics.SourceValidatorLatencyEMA, metrics.SourceFailureSchema)
		} else {
			metrics.MarkSourceReadOutcome(metrics.SourceValidatorLatencyEMA, true)
			metrics.MarkSourceSchemaOutcome(metrics.SourceValidatorLatencyEMA, true)
		}
		return nil
	}
	if now.Sub(candidateTime) > validatorEMAStaleAfter {
		m.lastEMATime = candidateTime
		m.withdrawEMA(false)
		metrics.MarkSourceReadOutcome(metrics.SourceValidatorLatencyEMA, true)
		metrics.MarkSourceSchemaOutcome(metrics.SourceValidatorLatencyEMA, true)
		metrics.MarkSourcePublication(metrics.SourceValidatorLatencyEMA)
		metrics.MarkMonitorPublication("validator_latency")
		if invalid {
			metrics.MarkSourceError(metrics.SourceValidatorLatencyEMA, metrics.SourceFailureSchema)
		}
		return nil
	}
	metrics.ReplaceValidatorLatencyEMA(candidateSnapshot)
	metrics.SetValidatorLatencyEMAState(candidateState)
	m.lastEMATime = candidateTime
	metrics.MarkSourceValidObservation(metrics.SourceValidatorLatencyEMA, candidateTime)
	metrics.MarkSourcePublication(metrics.SourceValidatorLatencyEMA)
	metrics.MarkMonitorValidObservation("validator_latency")
	metrics.MarkMonitorPublication("validator_latency")
	if invalid {
		metrics.MarkSourceError(metrics.SourceValidatorLatencyEMA, metrics.SourceFailureSchema)
	}

	return nil
}

func (m *ValidatorLatencyMonitor) withdrawEMA(reset bool) {
	metrics.ReplaceValidatorLatencyEMA(nil)
	for _, state := range []string{"measured", "initializing", "no_data", "invalid"} {
		metrics.HLConsensusValidatorLatencyEMAState.DeleteLabelValues(state)
	}
	if reset {
		m.emaTail = fileTailState{}
		m.lastEMATime = time.Time{}
	}
}

func parseValidatorEMASnapshot(line []byte) (time.Time, string, map[string]float64, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return time.Time{}, "invalid", nil, fmt.Errorf("failed to parse EMA line: %w", err)
	}
	if len(outer) != 2 {
		return time.Time{}, "invalid", nil, fmt.Errorf("unexpected EMA format")
	}
	var timestamp string
	if err := unmarshalRequiredJSON(outer[0], &timestamp); err != nil {
		return time.Time{}, "invalid", nil, fmt.Errorf("failed to parse EMA timestamp: %w", err)
	}
	entryTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
	if err != nil {
		return time.Time{}, "invalid", nil, fmt.Errorf("failed to parse EMA time: %w", err)
	}
	var rows []json.RawMessage
	if !isJSONArray(outer[1]) {
		return time.Time{}, "invalid", nil, fmt.Errorf("EMA rows are not an array")
	}
	if err := json.Unmarshal(outer[1], &rows); err != nil {
		return time.Time{}, "invalid", nil, fmt.Errorf("failed to parse EMA rows: %w", err)
	}
	type parsedRow struct {
		validator string
		value     float64
	}
	parsed := make([]parsedRow, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	allZero := len(rows) > 0
	for i, rawRow := range rows {
		var row []json.RawMessage
		if err := json.Unmarshal(rawRow, &row); err != nil || len(row) != 2 {
			return time.Time{}, "invalid", nil, fmt.Errorf("invalid EMA row %d", i)
		}
		var validator string
		var value float64
		if err := unmarshalRequiredJSON(row[0], &validator); err != nil || strings.TrimSpace(validator) == "" {
			return time.Time{}, "invalid", nil, fmt.Errorf("invalid EMA validator %d", i)
		}
		validator = strings.ToLower(validator)
		if _, duplicate := seen[validator]; duplicate {
			return time.Time{}, "invalid", nil, fmt.Errorf("duplicate EMA validator")
		}
		if err := unmarshalRequiredJSON(row[1], &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return time.Time{}, "invalid", nil, fmt.Errorf("invalid EMA value %d", i)
		}
		seen[validator] = struct{}{}
		parsed = append(parsed, parsedRow{validator: validator, value: value})
		if value != 0 {
			allZero = false
		}
	}
	if allZero {
		return entryTime, "initializing", map[string]float64{}, nil
	}
	const emaNoDataSentinel = 0.4
	snapshot := make(map[string]float64, len(parsed))
	for _, row := range parsed {
		if row.value >= emaNoDataSentinel-1e-9 && row.value <= emaNoDataSentinel+1e-9 {
			continue
		}
		snapshot[row.validator] = row.value
	}
	state := "measured"
	if len(snapshot) == 0 {
		state = "no_data"
	}
	return entryTime, state, snapshot, nil
}
