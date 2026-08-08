package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// visorState mirrors the JSON record the visor writes to disk. The schema
// has been stable across hl-node releases since Aug 2025; we tolerate extra
// fields and missing fields gracefully.
type visorState struct {
	InitialHeight         int64    `json:"initial_height"`
	Height                int64    `json:"height"`
	ScheduledFreezeHeight *int64   `json:"scheduled_freeze_height"`
	HardforkVersion       *int64   `json:"hardfork_version"`
	ConsensusTime         string   `json:"consensus_time"`
	WallClockTime         string   `json:"wall_clock_time"`
	ReferenceLagSeconds   *float64 `json:"reference_lag_seconds,omitempty"`
	ReferenceLag          *float64 `json:"reference_lag,omitempty"`
}

const visorPollInterval = 10 * time.Second

// StartVisorMonitor watches the visor sync state. Two sources, both
// updated by hl-visor:
//
//   - $NODE_HOME/hyperliquid_data/visor_abci_state.json: pretty-printed
//     JSON snapshot, kept current. Cheap to stat and re-read.
//   - $NODE_HOME/data/visor_abci_states/hourly/<YYYYMMDD>/<H>: rolling
//     JSON-line log. Used as a fallback if the live snapshot is missing.
//
// Exposes hl_visor_height and the lag/skew metrics. This is the
// most important sync-health signal for a Hyperliquid node operator.
func StartVisorMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceVisor, true)
	snapshotPath := filepath.Join(cfg.NodeHome, "hyperliquid_data", "visor_abci_state.json")
	historicalDir := filepath.Join(cfg.NodeHome, "data", "visor_abci_states", "hourly")

	logger.InfoComponent("visor", "starting visor state monitor: snapshot=%s historical=%s",
		snapshotPath, historicalDir)

	ticker := time.NewTicker(visorPollInterval)
	defer ticker.Stop()

	// Seed the companion gauge so it's present (0) before the first sample
	// that carries a reference_lag field — distinguishes "genuinely 0" from
	// "never reported". publishVisorState flips it to 1 when the field exists.
	metrics.HLVisorReferenceLagPopulated.Set(0)

	// Run once immediately so the first scrape isn't empty.
	tickVisor(snapshotPath, historicalDir, errCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickVisor(snapshotPath, historicalDir, errCh)
		}
	}
}

func tickVisor(snapshotPath, historicalDir string, errCh chan<- error) bool {
	metrics.MarkMonitorAttempt("visor")
	metrics.MarkSourceAttempt(metrics.SourceVisor)
	state, ts, err := readLatestVisorState(snapshotPath, historicalDir)
	if err != nil {
		// Don't flood errCh on a fresh / mid-restart node — debug-log and
		// move on. The exporter health gauges will reflect the missing tick.
		logger.DebugComponent("visor", "no visor state yet: %v", err)
		if os.IsNotExist(err) {
			metrics.MarkSourceAbsent(metrics.SourceVisor)
		} else {
			metrics.MarkSourceError(metrics.SourceVisor, metrics.SourceFailureRead)
		}
		return false
	}
	publishVisorState(state, ts)
	metrics.MarkSourceValidObservation(metrics.SourceVisor, ts)
	metrics.MarkSourcePublication(metrics.SourceVisor)
	metrics.MarkMonitorValidObservation("visor")
	metrics.MarkMonitorPublication("visor")
	return true
}

func readLatestVisorState(snapshotPath, historicalDir string) (visorState, time.Time, error) {
	// Prefer the live JSON snapshot: it's always the most recent.
	if data, err := os.ReadFile(snapshotPath); err == nil {
		if s, jerr := decodeVisorState(data); jerr == nil && s.Height > 0 {
			// Sample timestamp comes from the JSON's wall_clock_time
			// field — the visor writes it at the moment it records the
			// sample. File mtime would be subject to write-buffering
			// latency. Fall back to mtime then now() if wall_clock_time
			// is missing or unparseable.
			if t, ok := parseVisorTime(s.WallClockTime); ok {
				return s, t, nil
			}
			info, _ := os.Stat(snapshotPath)
			if info != nil {
				return s, info.ModTime(), nil
			}
			return s, time.Now(), nil
		}
	}

	// Fallback: tail the latest hourly file. Format per line:
	//   ["<iso>", {<state>}]
	latest, err := latestHourlyFile(historicalDir)
	if err != nil {
		return visorState{}, time.Time{}, err
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return visorState{}, time.Time{}, err
	}
	// take the last newline-terminated record
	for i := len(data) - 1; i > 0; i-- {
		if data[i] == '\n' && i+1 < len(data) {
			data = data[i+1:]
			break
		}
		if i == 1 {
			break
		}
	}
	var record []json.RawMessage
	if err := json.Unmarshal(data, &record); err != nil {
		return visorState{}, time.Time{}, fmt.Errorf("decode record: %w", err)
	}
	if len(record) != 2 {
		return visorState{}, time.Time{}, fmt.Errorf("decode record: expected 2 fields, got %d", len(record))
	}
	var tsStr string
	if err := json.Unmarshal(record[0], &tsStr); err != nil {
		return visorState{}, time.Time{}, fmt.Errorf("decode ts: %w", err)
	}
	s, err := decodeVisorState(record[1])
	if err != nil {
		return visorState{}, time.Time{}, fmt.Errorf("decode state: %w", err)
	}
	if s.Height <= 0 {
		return visorState{}, time.Time{}, fmt.Errorf("decode state: invalid height %d", s.Height)
	}
	ts, ok := parseVisorTime(tsStr)
	if !ok {
		return visorState{}, time.Time{}, fmt.Errorf("invalid historical timestamp")
	}
	return s, ts, nil
}

// decodeVisorState treats independently optional scalar fields as
// independently available. In particular, an older or transitional node that
// writes hardfork_version with an unexpected type must not suppress the valid
// height and timing fields in the same state generation.
func decodeVisorState(data []byte) (visorState, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return visorState{}, fmt.Errorf("decode visor object: %w", err)
	}
	var s visorState
	decode := func(key string, dst interface{}) error {
		raw, ok := fields[key]
		if !ok {
			return nil
		}
		return json.Unmarshal(raw, dst)
	}
	if err := decode("initial_height", &s.InitialHeight); err != nil {
		return visorState{}, fmt.Errorf("decode initial_height: %w", err)
	}
	if err := decode("height", &s.Height); err != nil {
		return visorState{}, fmt.Errorf("decode height: %w", err)
	}
	_ = decode("consensus_time", &s.ConsensusTime)
	_ = decode("wall_clock_time", &s.WallClockTime)
	s.ScheduledFreezeHeight = decodeOptionalVisorInt(fields["scheduled_freeze_height"])
	s.HardforkVersion = decodeOptionalVisorInt(fields["hardfork_version"])
	s.ReferenceLagSeconds = decodeOptionalVisorFloat(fields["reference_lag_seconds"])
	s.ReferenceLag = decodeOptionalVisorFloat(fields["reference_lag"])
	return s, nil
}

func decodeOptionalVisorInt(raw json.RawMessage) *int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return nil
	}
	return &value
}

func decodeOptionalVisorFloat(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func publishVisorState(s visorState, sampleTime time.Time) {
	metrics.HLVisorHeight.Set(float64(s.Height))
	metrics.HLVisorInitialHeight.Set(float64(s.InitialHeight))
	// Surface the latest height to node_state_monitor so it can derive
	// hl_visor_blocks_above_freeze without re-reading the JSON.
	SetLatestVisorHeight(s.Height)
	if s.Height > 0 && s.InitialHeight > 0 && s.Height >= s.InitialHeight {
		metrics.HLVisorBlocksApplied.Set(float64(s.Height - s.InitialHeight))
	} else {
		metrics.HLVisorBlocksApplied.Set(0)
	}
	if s.ScheduledFreezeHeight != nil && *s.ScheduledFreezeHeight >= 0 {
		metrics.HLVisorScheduledFreezeHeight.Set(float64(*s.ScheduledFreezeHeight))
		metrics.HLVisorScheduledFreezeHeightCurrent.WithLabelValues("visor_state").Set(float64(*s.ScheduledFreezeHeight))
		metrics.HLVisorScheduledFreezeHeightAvailable.Set(1)
	} else {
		metrics.HLVisorScheduledFreezeHeight.Set(0)
		metrics.HLVisorScheduledFreezeHeightCurrent.DeleteLabelValues("visor_state")
		metrics.HLVisorScheduledFreezeHeightAvailable.Set(0)
	}
	if s.HardforkVersion != nil && *s.HardforkVersion >= 0 {
		metrics.HLVisorHardforkVersion.WithLabelValues("visor_state").Set(float64(*s.HardforkVersion))
		metrics.HLVisorHardforkVersionAvailable.Set(1)
	} else {
		metrics.HLVisorHardforkVersion.DeleteLabelValues("visor_state")
		metrics.HLVisorHardforkVersionAvailable.Set(0)
	}

	consensusT, okC := parseVisorTime(s.ConsensusTime)
	wallT, okW := parseVisorTime(s.WallClockTime)
	if okC && okW {
		metrics.HLVisorConsensusAheadOfWallSeconds.Set(consensusT.Sub(wallT).Seconds())
	}

	switch {
	case s.ReferenceLagSeconds != nil:
		metrics.HLVisorReferenceLagSeconds.Set(*s.ReferenceLagSeconds)
		metrics.HLVisorReferenceLagPopulated.Set(1)
	case s.ReferenceLag != nil:
		metrics.HLVisorReferenceLagSeconds.Set(*s.ReferenceLag)
		metrics.HLVisorReferenceLagPopulated.Set(1)
	default:
		metrics.HLVisorReferenceLagPopulated.Set(0)
	}

	if !sampleTime.IsZero() {
		age := time.Since(sampleTime).Seconds()
		if age < 0 {
			age = 0
		}
		metrics.HLVisorLastObservationAge.Set(age)
	}
}

// parseVisorTime accepts the few timestamp shapes hl-visor uses (RFC3339Nano,
// fractional-second RFC3339, plain ISO without zone). Returns ok=false if
// none parse.
func parseVisorTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// latestHourlyFile finds the newest file under a .../hourly/<YYYYMMDD>/<H>
// layout. Date directories are YYYYMMDD-named, so lexicographic order
// equals chronological order. Hour entries are named "0".."23" with no
// leading zero, so a naive lex sort would put "10" before "2"; we sort
// numerically. Falls back to lex if a name is non-numeric.
func latestHourlyFile(root string) (string, error) {
	dateDirs, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	dateNames := make([]string, 0, len(dateDirs))
	for _, e := range dateDirs {
		if e.IsDir() {
			dateNames = append(dateNames, e.Name())
		}
	}
	sort.Strings(dateNames)
	for i := len(dateNames) - 1; i >= 0; i-- {
		datePath := filepath.Join(root, dateNames[i])
		hourEntries, err := os.ReadDir(datePath)
		if err != nil || len(hourEntries) == 0 {
			continue
		}
		hourNames := make([]string, 0, len(hourEntries))
		for _, e := range hourEntries {
			hourNames = append(hourNames, e.Name())
		}
		sort.Slice(hourNames, func(a, b int) bool {
			an, aerr := strconv.Atoi(hourNames[a])
			bn, berr := strconv.Atoi(hourNames[b])
			if aerr == nil && berr == nil {
				return an < bn
			}
			return hourNames[a] < hourNames[b]
		})
		return filepath.Join(datePath, hourNames[len(hourNames)-1]), nil
	}
	return "", os.ErrNotExist
}

// resolveLatestHourlyStream is the strict discovery path for incremental
// streams whose current gauges must be withdrawn only when the source root is
// confirmed missing or its complete readable tree is validly empty. Unlike
// latestHourlyFile, a nested traversal failure never falls back to an older
// date and never impersonates an empty source.
func resolveLatestHourlyStream(root string) tailStreamResolution {
	return resolveLatestHourlyStreamWith(root, os.ReadDir)
}

func resolveLatestHourlyStreamWith(root string, readDir func(string) ([]os.DirEntry, error)) tailStreamResolution {
	dateDirs, err := readDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tailStreamResolution{unavailable: tailStreamUnavailableAbsent}
		}
		return tailStreamResolution{err: err}
	}
	dateNames := make([]string, 0, len(dateDirs))
	for _, entry := range dateDirs {
		if entry.IsDir() {
			dateNames = append(dateNames, entry.Name())
		}
	}
	sort.Strings(dateNames)
	for i := len(dateNames) - 1; i >= 0; i-- {
		datePath := filepath.Join(root, dateNames[i])
		hourEntries, err := readDir(datePath)
		if err != nil {
			return tailStreamResolution{err: err}
		}
		if len(hourEntries) == 0 {
			continue
		}
		hourNames := make([]string, 0, len(hourEntries))
		for _, entry := range hourEntries {
			hourNames = append(hourNames, entry.Name())
		}
		sort.Slice(hourNames, func(a, b int) bool {
			an, aerr := strconv.Atoi(hourNames[a])
			bn, berr := strconv.Atoi(hourNames[b])
			if aerr == nil && berr == nil {
				return an < bn
			}
			return hourNames[a] < hourNames[b]
		})
		return tailStreamResolution{path: filepath.Join(datePath, hourNames[len(hourNames)-1])}
	}
	return tailStreamResolution{unavailable: tailStreamUnavailableEmpty}
}
