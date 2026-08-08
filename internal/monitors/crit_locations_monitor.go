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

// The surviving paired producer is hl-visor. It is deliberately not a
// fallback for hl-node: the daily visor row and this rich visor document must
// describe one matched generation before locations can be replaced.
const critLocationsPath = "/tmp/crit_msg_latest_stats/hl-visor.json"

const critLocationsPollInterval = 60 * time.Second

// Bounded cardinality: only basename + line from the top-N locations. Message
// text (including first_msg) is never parsed into labels or metric values.
const critLocationCap = 32

type critMsgRichFile struct {
	StartTime string              `json:"start_time"`
	NBugs     int64               `json:"n_bugs"`
	NCrits    int64               `json:"n_crits"`
	Stats     [][]json.RawMessage `json:"code_location_and_stats"`
}

type critLocation struct {
	File      string `json:"fln"`
	Line      int64  `json:"line"`
	N         int64  `json:"n"`
	IsIgnored bool   `json:"is_ignored"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

type critRichSnapshot struct {
	baseTime time.Time
	nBugs    int64
	nCrits   int64
	rawCount int64
	locs     []critLocation
}

type critLocationsState struct {
	store          *critGenerationStore
	active         map[[2]string]struct{}
	lastGeneration critGeneration
}

func StartCritLocationsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	_ = cfg
	logger.InfoComponent("crit_locations", "watching matched visor projection %s", critLocationsPath)
	metrics.RegisterSource(metrics.SourceCriticalLocations, true)
	state := &critLocationsState{store: sharedCritGenerations, active: make(map[[2]string]struct{})}

	ticker := time.NewTicker(critLocationsPollInterval)
	defer ticker.Stop()
	if err := tickCritLocationsAt(critLocationsPath, state, time.Now()); err != nil {
		logger.DebugComponent("crit_locations", "initial poll: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tickCritLocationsAt(critLocationsPath, state, time.Now()); err != nil {
				logger.DebugComponent("crit_locations", "poll: %v", err)
			}
		}
	}
}

func tickCritLocationsAt(path string, state *critLocationsState, now time.Time) error {
	const source = "hl-visor"
	metrics.MarkMonitorAttempt("crit_locations")
	metrics.MarkSourceAttempt(metrics.SourceCriticalLocations)
	info, err := os.Stat(path)
	if err != nil {
		metrics.SetCriticalMessageProjectionState(source, "rich", false, -1)
		metrics.HLCriticalMessageGenerationMatch.WithLabelValues(source).Set(0)
		if os.IsNotExist(err) {
			metrics.MarkSourceAbsent(metrics.SourceCriticalLocations)
			return nil
		}
		metrics.MarkSourceError(metrics.SourceCriticalLocations, metrics.SourceFailureStat)
		return fmt.Errorf("stat rich critical-message projection: %w", err)
	}
	if age := now.Sub(info.ModTime()); age > critMsgStaleAfter {
		metrics.SetCriticalMessageProjectionState(source, "rich", false, 1)
		metrics.HLCriticalMessageGenerationMatch.WithLabelValues(source).Set(0)
		metrics.MarkSourceReadOutcome(metrics.SourceCriticalLocations, true)
		metrics.MarkSourceSchemaOutcome(metrics.SourceCriticalLocations, true)
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		metrics.SetCriticalMessageProjectionState(source, "rich", false, -1)
		metrics.HLCriticalMessageGenerationMatch.WithLabelValues(source).Set(0)
		metrics.MarkSourceError(metrics.SourceCriticalLocations, metrics.SourceFailureRead)
		return fmt.Errorf("read rich critical-message projection: %w", err)
	}
	snapshot, err := parseCritRichSnapshot(data)
	if err != nil {
		metrics.SetCriticalMessageProjectionState(source, "rich", false, 0)
		metrics.HLCriticalMessageGenerationMatch.WithLabelValues(source).Set(0)
		metrics.MarkSourceError(metrics.SourceCriticalLocations, metrics.SourceFailureSchema)
		return err
	}
	generationChanged := false
	daily, matched := state.store.withAvailable(source, func(generation critGeneration) bool {
		if !critRichMatchesDaily(snapshot, info.ModTime(), generation) {
			return false
		}
		generationChanged = !state.lastGeneration.same(generation)
		replaceCritLocations(state, snapshot.locs)
		state.lastGeneration = generation
		return true
	})
	if !matched {
		metrics.SetCriticalMessageProjectionState(source, "rich", false, 1)
		metrics.HLCriticalMessageGenerationMatch.WithLabelValues(source).Set(0)
		metrics.MarkSourceError(metrics.SourceCriticalLocations, metrics.SourceFailureSchema)
		return fmt.Errorf("rich critical-message projection does not match the current daily generation")
	}

	metrics.SetCriticalMessageProjectionState(source, "rich", true, 1)
	metrics.HLCriticalMessageGenerationMatch.WithLabelValues(source).Set(1)
	if generationChanged {
		metrics.MarkSourceValidObservation(metrics.SourceCriticalLocations, daily.SampleTime)
		metrics.MarkMonitorValidObservation("crit_locations")
	}
	metrics.MarkSourcePublication(metrics.SourceCriticalLocations)
	metrics.MarkMonitorPublication("crit_locations")
	return nil
}

func parseCritRichSnapshot(data []byte) (critRichSnapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return critRichSnapshot{}, fmt.Errorf("decode rich critical-message projection: %w", err)
	}
	var rich critMsgRichFile
	for _, field := range []struct {
		name string
		dst  any
	}{
		{"start_time", &rich.StartTime}, {"n_bugs", &rich.NBugs}, {"n_crits", &rich.NCrits}, {"code_location_and_stats", &rich.Stats},
	} {
		if err := unmarshalRequiredJSON(raw[field.name], field.dst); err != nil {
			return critRichSnapshot{}, fmt.Errorf("required rich field %s: %w", field.name, err)
		}
	}
	baseTime, ok := parseVisorTime(rich.StartTime)
	if !ok || rich.NBugs < 0 || rich.NCrits < 0 || rich.Stats == nil {
		return critRichSnapshot{}, fmt.Errorf("invalid rich critical-message header")
	}
	byLabel := make(map[[2]string]critLocation, len(rich.Stats))
	for i, pair := range rich.Stats {
		if len(pair) != 2 {
			return critRichSnapshot{}, fmt.Errorf("invalid rich location pair %d", i)
		}
		var keyRaw, detailRaw map[string]json.RawMessage
		if err := unmarshalRequiredJSON(pair[0], &keyRaw); err != nil {
			return critRichSnapshot{}, fmt.Errorf("invalid rich location key %d", i)
		}
		if err := unmarshalRequiredJSON(pair[1], &detailRaw); err != nil {
			return critRichSnapshot{}, fmt.Errorf("invalid rich location detail %d", i)
		}
		var key struct {
			File string
			Line int64
		}
		var detail struct {
			N         int64
			IsIgnored bool
			FirstSeen string
			LastSeen  string
		}
		for _, field := range []struct {
			name string
			dst  any
		}{{"fln", &key.File}, {"line", &key.Line}} {
			if err := unmarshalRequiredJSON(keyRaw[field.name], field.dst); err != nil {
				return critRichSnapshot{}, fmt.Errorf("invalid rich location key field %s at %d", field.name, i)
			}
		}
		for _, field := range []struct {
			name string
			dst  any
		}{{"n", &detail.N}, {"is_ignored", &detail.IsIgnored}, {"first_seen", &detail.FirstSeen}, {"last_seen", &detail.LastSeen}} {
			if err := unmarshalRequiredJSON(detailRaw[field.name], field.dst); err != nil {
				return critRichSnapshot{}, fmt.Errorf("invalid rich location detail field %s at %d", field.name, i)
			}
		}
		if key.File == "" || key.Line < 0 {
			return critRichSnapshot{}, fmt.Errorf("invalid rich location key %d", i)
		}
		if detail.N < 0 {
			return critRichSnapshot{}, fmt.Errorf("invalid rich location detail %d", i)
		}
		if _, ok := parseVisorTime(detail.LastSeen); !ok {
			return critRichSnapshot{}, fmt.Errorf("invalid rich last_seen %d", i)
		}
		location := critLocation{File: filepath.Base(key.File), Line: key.Line, N: detail.N, IsIgnored: detail.IsIgnored, FirstSeen: detail.FirstSeen, LastSeen: detail.LastSeen}
		label := [2]string{location.File, strconv.FormatInt(location.Line, 10)}
		// Basename normalization can collide across source directories. Keep
		// one deterministic maximum; never sum locations or message text.
		if previous, exists := byLabel[label]; !exists || location.N > previous.N {
			byLabel[label] = location
		}
	}
	locs := make([]critLocation, 0, len(byLabel))
	for _, location := range byLabel {
		locs = append(locs, location)
	}
	sort.Slice(locs, func(i, j int) bool {
		if locs[i].N != locs[j].N {
			return locs[i].N > locs[j].N
		}
		if locs[i].File != locs[j].File {
			return locs[i].File < locs[j].File
		}
		return locs[i].Line < locs[j].Line
	})
	if len(locs) > critLocationCap {
		locs = locs[:critLocationCap]
	}
	return critRichSnapshot{baseTime: baseTime, nBugs: rich.NBugs, nCrits: rich.NCrits, rawCount: int64(len(rich.Stats)), locs: locs}, nil
}

func critRichMatchesDaily(rich critRichSnapshot, mtime time.Time, daily critGeneration) bool {
	skew := mtime.Sub(daily.SampleTime)
	if skew < 0 {
		skew = -skew
	}
	return daily.Source == "hl-visor" &&
		rich.baseTime.Equal(daily.BaseTime) &&
		rich.nBugs == daily.NBugs &&
		rich.nCrits == daily.NCrits &&
		rich.rawCount == daily.NLocations &&
		skew <= critMsgGenerationSkew
}

func replaceCritLocations(state *critLocationsState, locations []critLocation) {
	current := make(map[[2]string]struct{}, len(locations))
	for _, location := range locations {
		line := strconv.FormatInt(location.Line, 10)
		label := [2]string{location.File, line}
		current[label] = struct{}{}
		metrics.HLNodeCritLocation.WithLabelValues(location.File, line).Set(float64(location.N))
		ignored := 0.0
		if location.IsIgnored {
			ignored = 1
		}
		metrics.HLNodeCritLocationIgnored.WithLabelValues(location.File, line).Set(ignored)
		lastSeen, _ := parseVisorTime(location.LastSeen)
		metrics.HLNodeCritLocationLastSeenSeconds.WithLabelValues(location.File, line).Set(float64(lastSeen.Unix()))
	}
	for label := range state.active {
		if _, ok := current[label]; !ok {
			metrics.HLNodeCritLocation.DeleteLabelValues(label[0], label[1])
			metrics.HLNodeCritLocationLastSeenSeconds.DeleteLabelValues(label[0], label[1])
			metrics.HLNodeCritLocationIgnored.DeleteLabelValues(label[0], label[1])
		}
	}
	state.active = current
}
