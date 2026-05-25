package monitors

import (
	"context"
	"encoding/json"
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
	InitialHeight          int64   `json:"initial_height"`
	Height                 int64   `json:"height"`
	ScheduledFreezeHeight  *int64  `json:"scheduled_freeze_height"`
	ConsensusTime          string  `json:"consensus_time"`
	WallClockTime          string  `json:"wall_clock_time"`
	ReferenceLagSeconds    *float64 `json:"reference_lag_seconds,omitempty"`
	ReferenceLag           *float64 `json:"reference_lag,omitempty"`
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
	snapshotPath := filepath.Join(cfg.NodeHome, "hyperliquid_data", "visor_abci_state.json")
	historicalDir := filepath.Join(cfg.NodeHome, "data", "visor_abci_states", "hourly")

	logger.InfoComponent("visor", "starting visor state monitor: snapshot=%s historical=%s",
		snapshotPath, historicalDir)

	ticker := time.NewTicker(visorPollInterval)
	defer ticker.Stop()

	// Run once immediately so the first scrape isn't empty.
	tickVisor(snapshotPath, historicalDir, errCh)
	metrics.MarkMonitorTick("visor")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickVisor(snapshotPath, historicalDir, errCh)
			metrics.MarkMonitorTick("visor")
		}
	}
}

func tickVisor(snapshotPath, historicalDir string, errCh chan<- error) {
	state, ts, err := readLatestVisorState(snapshotPath, historicalDir)
	if err != nil {
		// Don't flood errCh on a fresh / mid-restart node — debug-log and
		// move on. The exporter health gauges will reflect the missing tick.
		logger.DebugComponent("visor", "no visor state yet: %v", err)
		return
	}
	publishVisorState(state, ts)
}

func readLatestVisorState(snapshotPath, historicalDir string) (visorState, time.Time, error) {
	// Prefer the live JSON snapshot: it's always the most recent.
	if data, err := os.ReadFile(snapshotPath); err == nil {
		var s visorState
		if jerr := json.Unmarshal(data, &s); jerr == nil && s.Height > 0 {
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
	var record [2]json.RawMessage
	if err := json.Unmarshal(data, &record); err != nil {
		return visorState{}, time.Time{}, fmt.Errorf("decode record: %w", err)
	}
	var tsStr string
	if err := json.Unmarshal(record[0], &tsStr); err != nil {
		return visorState{}, time.Time{}, fmt.Errorf("decode ts: %w", err)
	}
	var s visorState
	if err := json.Unmarshal(record[1], &s); err != nil {
		return visorState{}, time.Time{}, fmt.Errorf("decode state: %w", err)
	}
	ts, _ := parseVisorTime(tsStr)
	return s, ts, nil
}

func publishVisorState(s visorState, sampleTime time.Time) {
	metrics.HLVisorHeight.Set(float64(s.Height))
	metrics.HLVisorInitialHeight.Set(float64(s.InitialHeight))
	// Surface the latest height to node_state_monitor so it can derive
	// hl_visor_blocks_above_freeze without re-reading the JSON.
	SetLatestVisorHeight(s.Height)
	if s.Height > 0 && s.InitialHeight > 0 {
		metrics.HLVisorBlocksApplied.Set(float64(s.Height - s.InitialHeight))
	}
	if s.ScheduledFreezeHeight != nil {
		metrics.HLVisorScheduledFreezeHeight.Set(float64(*s.ScheduledFreezeHeight))
	} else {
		metrics.HLVisorScheduledFreezeHeight.Set(0)
	}

	consensusT, okC := parseVisorTime(s.ConsensusTime)
	wallT, okW := parseVisorTime(s.WallClockTime)
	if okC && okW {
		metrics.HLVisorConsensusAheadOfWallSeconds.Set(consensusT.Sub(wallT).Seconds())
	}

	switch {
	case s.ReferenceLagSeconds != nil:
		metrics.HLVisorReferenceLagSeconds.Set(*s.ReferenceLagSeconds)
	case s.ReferenceLag != nil:
		metrics.HLVisorReferenceLagSeconds.Set(*s.ReferenceLag)
	}

	if !sampleTime.IsZero() {
		metrics.HLVisorLastObservationAge.Set(time.Since(sampleTime).Seconds())
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
