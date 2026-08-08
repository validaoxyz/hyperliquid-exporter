package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// critMsgSources lists the subdirectories under crit_msg_stats/ that we
// publish. Both come standard with hl-node and hl-visor.
var critMsgSources = []string{"hl-node", "hl-visor"}

// crit_msg_stats files are appended every ~5 minutes by the node. We poll
// at 30s so the latest record never sits more than half a sample interval
// stale, but skip work when nothing has changed using the file's mtime.
const critMsgPollInterval = 30 * time.Second

var errNoCompleteCritMessage = errors.New("no complete critical-message record")

type critMsgMonitorState struct {
	store     *critGenerationStore
	published map[string]struct{}
}

// StartCritMsgMonitor watches $NODE_HOME/data/crit_msg_stats/<source>/<YYYYMMDD>.
// File contents are JSON-line records of the form
//
//	[<sample_iso>, [<base_iso>, n_bugs, n_crits, n_locations]]
//
// where the three integer counts come from hl-node's crit_msg.rs:
//
//   - n_bugs      — cumulative count of bug! events
//   - n_crits     — cumulative count of crit! events
//   - n_locations — distinct (file, line) call sites that have fired a
//     bug or crit at least once since the process started
//
// All counts reset to 0 on process restart, so we model them as gauges,
// which lets a restart cleanly zero the series instead of producing a
// counter regression.
//
// Operator semantics:
//   - n_bugs > 0 is the strongest "page someone" signal hl-node emits.
//   - rate(n_crits) > 0 over a few minutes = ongoing incident.
//   - n_locations growing means a NEW crit started firing (vs the same
//     recurring one), which is useful for routing the alert.
func StartCritMsgMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "crit_msg_stats")
	logger.InfoComponent("crit_msg", "watching %s", root)
	metrics.RegisterSource(metrics.SourceCriticalMessages, true)
	state := &critMsgMonitorState{store: sharedCritGenerations, published: make(map[string]struct{})}

	ticker := time.NewTicker(critMsgPollInterval)
	defer ticker.Stop()

	tickCritMsg(root, state, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickCritMsg(root, state, time.Now())
		}
	}
}

func tickCritMsg(root string, state *critMsgMonitorState, now time.Time) {
	metrics.MarkMonitorAttempt("crit_msg")
	metrics.MarkSourceAttempt(metrics.SourceCriticalMessages)
	visorAvailable := false
	visorConfirmedAbsent := false
	for _, source := range critMsgSources {
		dir := filepath.Join(root, source)
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				withdrawCritMessageSource(state, source)
				state.store.markUnavailable(source)
				metrics.SetCriticalMessageProjectionState(source, "daily", false, -1)
				if source == "hl-visor" {
					visorConfirmedAbsent = true
				}
			} else {
				state.store.markUnavailable(source)
				metrics.SetCriticalMessageProjectionState(source, "daily", false, -1)
				if source == "hl-visor" {
					metrics.MarkSourceError(metrics.SourceCriticalMessages, metrics.SourceFailureStat)
				}
			}
			continue
		}
		datePath, err := latestDateFile(dir)
		if err != nil {
			if os.IsNotExist(err) {
				withdrawCritMessageSource(state, source)
				state.store.markUnavailable(source)
				metrics.SetCriticalMessageProjectionState(source, "daily", false, -1)
				if source == "hl-visor" {
					visorConfirmedAbsent = true
				}
			} else {
				state.store.markUnavailable(source)
				metrics.SetCriticalMessageProjectionState(source, "daily", false, -1)
				if source == "hl-visor" {
					metrics.MarkSourceError(metrics.SourceCriticalMessages, metrics.SourceFailureRead)
				}
			}
			continue
		}
		generation, err := readLastCritGeneration(datePath, source)
		if err != nil {
			state.store.markUnavailable(source)
			parseState := 0
			if errors.Is(err, errNoCompleteCritMessage) {
				parseState = -1
			}
			metrics.SetCriticalMessageProjectionState(source, "daily", false, parseState)
			if source == "hl-visor" {
				stage := metrics.SourceFailureSchema
				if errors.Is(err, os.ErrPermission) {
					stage = metrics.SourceFailureRead
				}
				if errors.Is(err, errNoCompleteCritMessage) {
					metrics.MarkSourceAvailable(metrics.SourceCriticalMessages)
				} else {
					metrics.MarkSourceError(metrics.SourceCriticalMessages, stage)
				}
			}
			continue
		}
		if now.Sub(generation.SampleTime) > critMsgStaleAfter {
			withdrawCritMessageSource(state, source)
			state.store.markUnavailable(source)
			metrics.SetCriticalMessageProjectionState(source, "daily", false, 1)
			if source == "hl-visor" {
				metrics.MarkSourceReadOutcome(metrics.SourceCriticalMessages, true)
				metrics.MarkSourceSchemaOutcome(metrics.SourceCriticalMessages, true)
			}
			continue
		}
		previous, hadPrevious := state.store.get(source)
		publishCritMsg(source, generation.BaseTime, generation.NBugs, generation.NCrits, generation.NLocations)
		metrics.HLCriticalMessageSampleTimestamp.WithLabelValues(source).Set(float64(generation.SampleTime.Unix()))
		state.published[source] = struct{}{}
		state.store.set(source, critDailyProjection{generation: generation, available: true})
		metrics.SetCriticalMessageProjectionState(source, "daily", true, 1)
		if source == "hl-visor" {
			visorAvailable = true
			if !hadPrevious || !previous.generation.same(generation) || !previous.available {
				metrics.MarkSourceValidObservation(metrics.SourceCriticalMessages, generation.SampleTime)
				metrics.MarkMonitorValidObservation("crit_msg")
			}
			metrics.MarkSourcePublication(metrics.SourceCriticalMessages)
		}
	}
	if !visorAvailable && visorConfirmedAbsent {
		metrics.MarkSourceAbsent(metrics.SourceCriticalMessages)
	}
	metrics.MarkMonitorPublication("crit_msg")
}

// readLastCritMsg returns the most recent record's base_time and the three
// cumulative counts. Reads the whole file (these stay under a few KB per
// day) and scans backwards for the first complete line.
func readLastCritMsg(path string) (time.Time, int64, int64, int64, bool) {
	generation, err := readLastCritGeneration(path, "")
	return generation.BaseTime, generation.NBugs, generation.NCrits, generation.NLocations, err == nil
}

func readLastCritGeneration(path, source string) (critGeneration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return critGeneration{}, err
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return critGeneration{}, errNoCompleteCritMessage
	}
	complete := bytes.TrimSpace(data[:lastNL])
	if len(complete) == 0 {
		return critGeneration{}, errNoCompleteCritMessage
	}
	start := bytes.LastIndexByte(complete, '\n') + 1
	generation, err := parseCritGeneration(bytes.TrimSpace(complete[start:]), source)
	if err != nil {
		return critGeneration{}, fmt.Errorf("decode latest complete critical-message record: %w", err)
	}
	return generation, nil
}

func parseCritMsgLine(line []byte) (time.Time, int64, int64, int64, bool) {
	generation, err := parseCritGeneration(line, "")
	return generation.BaseTime, generation.NBugs, generation.NCrits, generation.NLocations, err == nil
}

func parseCritGeneration(line []byte, source string) (critGeneration, error) {
	if len(line) == 0 || line[0] != '[' {
		return critGeneration{}, fmt.Errorf("record is not an array")
	}
	// Expected: [<sample_ts>, [<base_ts>, n_bugs, n_crits, n_locations]]
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil || len(outer) != 2 {
		return critGeneration{}, fmt.Errorf("invalid outer tuple")
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) != 4 {
		return critGeneration{}, fmt.Errorf("invalid inner tuple")
	}
	var sampleStr, baseStr string
	if err := unmarshalRequiredJSON(outer[0], &sampleStr); err != nil {
		return critGeneration{}, fmt.Errorf("invalid sample timestamp")
	}
	if err := unmarshalRequiredJSON(inner[0], &baseStr); err != nil {
		return critGeneration{}, fmt.Errorf("invalid base timestamp")
	}
	sampleTime, okSample := parseVisorTime(sampleStr)
	baseTime, okBase := parseVisorTime(baseStr)
	if !okSample || !okBase {
		return critGeneration{}, fmt.Errorf("unparseable timestamp")
	}
	var nBugs, nCrits, nLocs int64
	if err := unmarshalRequiredJSON(inner[1], &nBugs); err != nil || nBugs < 0 {
		return critGeneration{}, fmt.Errorf("invalid bug count")
	}
	if err := unmarshalRequiredJSON(inner[2], &nCrits); err != nil || nCrits < 0 {
		return critGeneration{}, fmt.Errorf("invalid crit count")
	}
	if err := unmarshalRequiredJSON(inner[3], &nLocs); err != nil || nLocs < 0 {
		return critGeneration{}, fmt.Errorf("invalid location count")
	}
	return critGeneration{Source: source, SampleTime: sampleTime, BaseTime: baseTime, NBugs: nBugs, NCrits: nCrits, NLocations: nLocs}, nil
}

func publishCritMsg(source string, baseTime time.Time, nBugs, nCrits, nLocs int64) {
	metrics.HLNodeBugsTotal.WithLabelValues(source).Set(float64(nBugs))
	metrics.HLNodeCritsTotal.WithLabelValues(source).Set(float64(nCrits))
	metrics.HLNodeCritLocations.WithLabelValues(source).Set(float64(nLocs))
	if !baseTime.IsZero() {
		metrics.HLNodeCriticalMessagesBaseTime.
			WithLabelValues(source).
			Set(float64(baseTime.Unix()))
	}
}

func withdrawCritMessageSource(state *critMsgMonitorState, source string) {
	if _, ok := state.published[source]; !ok {
		return
	}
	metrics.HLNodeBugsTotal.DeleteLabelValues(source)
	metrics.HLNodeCritsTotal.DeleteLabelValues(source)
	metrics.HLNodeCritLocations.DeleteLabelValues(source)
	metrics.HLNodeCriticalMessagesBaseTime.DeleteLabelValues(source)
	metrics.HLCriticalMessageSampleTimestamp.DeleteLabelValues(source)
	delete(state.published, source)
}
