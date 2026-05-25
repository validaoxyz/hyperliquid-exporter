package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
//                   bug or crit at least once since the process started
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

	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("crit_msg",
			"crit_msg_stats directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("crit_msg", "watching %s", root)

	lastMtime := make(map[string]time.Time)

	ticker := time.NewTicker(critMsgPollInterval)
	defer ticker.Stop()

	tickCritMsg(root, lastMtime)
	metrics.MarkMonitorTick("crit_msg")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickCritMsg(root, lastMtime)
			metrics.MarkMonitorTick("crit_msg")
		}
	}
}

func tickCritMsg(root string, lastMtime map[string]time.Time) {
	for _, source := range critMsgSources {
		dir := filepath.Join(root, source)
		datePath, err := latestDateFile(dir)
		if err != nil {
			continue
		}
		info, err := os.Stat(datePath)
		if err != nil {
			continue
		}
		if prev, ok := lastMtime[source]; ok && info.ModTime().Equal(prev) {
			continue // unchanged since last tick; the published gauges are still current
		}
		baseTime, nBugs, nCrits, nLocs, ok := readLastCritMsg(datePath)
		if !ok {
			continue
		}
		publishCritMsg(source, baseTime, nBugs, nCrits, nLocs)
		lastMtime[source] = info.ModTime()
	}
}

// readLastCritMsg returns the most recent record's base_time and the three
// cumulative counts. Reads the whole file (these stay under a few KB per
// day) and scans backwards for the first complete line.
func readLastCritMsg(path string) (time.Time, int64, int64, int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, 0, 0, 0, false
	}
	for end := len(data); end > 0; {
		// Skip trailing newlines so a torn write doesn't park us on an empty
		// last "line".
		for end > 0 && data[end-1] == '\n' {
			end--
		}
		if end == 0 {
			break
		}
		start := bytes.LastIndexByte(data[:end], '\n') + 1
		line := bytes.TrimSpace(data[start:end])
		if bt, b, c, l, ok := parseCritMsgLine(line); ok {
			return bt, b, c, l, true
		}
		// Move to the previous line and try again (resilient to a torn last
		// write).
		end = start - 1
	}
	return time.Time{}, 0, 0, 0, false
}

func parseCritMsgLine(line []byte) (time.Time, int64, int64, int64, bool) {
	if len(line) == 0 || line[0] != '[' {
		return time.Time{}, 0, 0, 0, false
	}
	// Expected: [<sample_ts>, [<base_ts>, n_bugs, n_crits, n_locations]]
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return time.Time{}, 0, 0, 0, false
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) < 4 {
		return time.Time{}, 0, 0, 0, false
	}
	var baseStr string
	if err := json.Unmarshal(inner[0], &baseStr); err != nil {
		return time.Time{}, 0, 0, 0, false
	}
	baseTime, _ := parseVisorTime(baseStr)

	nBugs, errB := strconv.ParseInt(string(inner[1]), 10, 64)
	nCrits, errC := strconv.ParseInt(string(inner[2]), 10, 64)
	nLocs, errL := strconv.ParseInt(string(inner[3]), 10, 64)
	if errB != nil || errC != nil || errL != nil {
		return time.Time{}, 0, 0, 0, false
	}
	return baseTime, nBugs, nCrits, nLocs, true
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
