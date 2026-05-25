package monitors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// critLocationsPath is where hl-node writes the rich-form companion to
// the on-disk crit_msg_stats counters. Format (single JSON document):
//
//	{
//	  "start_time": "<iso>",
//	  "n_bugs":     <int>,
//	  "n_crits":    <int>,
//	  "code_location_and_stats": [
//	    [ {"fln": "<path>", "line": <int>},
//	      {"n": <int>, "is_ignored": <bool>,
//	       "first_seen": "<iso>", "last_seen": "<iso>",
//	       "first_msg": "<string>"} ],
//	    ...
//	  ]
//	}
//
// hl-visor.json is NOT produced by every node version — present on
// the test peer's filesystem only as hl-node.json. The monitor skips
// silently when the file doesn't exist.
const critLocationsPath = "/tmp/crit_msg_latest_stats/hl-node.json"

const critLocationsPollInterval = 60 * time.Second

// critLocationCap is the hard cardinality ceiling on the metric. The
// node naturally caps the array at ~10 elements on the test peer, but
// a misbehaving node could in principle grow it without bound. Above
// this cap we publish the top-N by count and drop the rest.
const critLocationCap = 32

// critLocationsMu protects activeLabels.
var (
	critLocationsMu sync.Mutex
	// activeLabels remembers which (file,line) tuples were published last
	// tick so we can Delete() any that fall out (mirrors the top-N
	// discipline used by tcp_traffic_monitor).
	activeLabels = map[[2]string]bool{}
)

// critMsgRichFile mirrors the on-disk schema.
type critMsgRichFile struct {
	StartTime string          `json:"start_time"`
	NBugs     int64           `json:"n_bugs"`
	NCrits    int64           `json:"n_crits"`
	Stats     [][]json.RawMessage `json:"code_location_and_stats"`
}

type critLocation struct {
	File       string `json:"fln"`
	Line       int64  `json:"line"`
	N          int64  `json:"n"`
	IsIgnored  bool   `json:"is_ignored"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	FirstMsg   string `json:"first_msg"`
}

// StartCritLocationsMonitor publishes per-source-location crit counts
// and last-seen timestamps. Useful for distinguishing "we have one
// recurring crit" from "a new crit type just appeared".
//
// The `file` label is the basename of the source path (not the full
// `/home/ubuntu/hl/code_Mainnet/...` path) to keep series names
// stable across hl-node releases that may relocate the source tree.
func StartCritLocationsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	if _, err := os.Stat(critLocationsPath); err != nil {
		logger.InfoComponent("crit_locations",
			"%s not present; monitor idle (this file is only written by some hl-node versions)", critLocationsPath)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("crit_locations", "watching %s", critLocationsPath)

	ticker := time.NewTicker(critLocationsPollInterval)
	defer ticker.Stop()

	tickCritLocations()
	metrics.MarkMonitorTick("crit_locations")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickCritLocations()
			metrics.MarkMonitorTick("crit_locations")
		}
	}
}

func tickCritLocations() {
	data, err := os.ReadFile(critLocationsPath)
	if err != nil {
		return
	}
	var rich critMsgRichFile
	if err := json.Unmarshal(data, &rich); err != nil {
		logger.DebugComponent("crit_locations", "parse: %v", err)
		return
	}

	locs := make([]critLocation, 0, len(rich.Stats))
	for _, pair := range rich.Stats {
		if len(pair) < 2 {
			continue
		}
		var key critLocation
		if err := json.Unmarshal(pair[0], &key); err != nil {
			continue
		}
		var detail critLocation
		if err := json.Unmarshal(pair[1], &detail); err != nil {
			continue
		}
		key.N = detail.N
		key.IsIgnored = detail.IsIgnored
		key.FirstSeen = detail.FirstSeen
		key.LastSeen = detail.LastSeen
		key.FirstMsg = detail.FirstMsg
		locs = append(locs, key)
	}

	if len(locs) == 0 {
		return
	}

	// Cap cardinality: keep the top-N by count.
	if len(locs) > critLocationCap {
		sort.Slice(locs, func(i, j int) bool { return locs[i].N > locs[j].N })
		locs = locs[:critLocationCap]
	}

	current := map[[2]string]bool{}
	for _, loc := range locs {
		file := filepath.Base(loc.File)
		line := fmt.Sprintf("%d", loc.Line)
		labels := [2]string{file, line}
		current[labels] = true

		metrics.HLNodeCritLocation.WithLabelValues(file, line).Set(float64(loc.N))
		if t, ok := parseVisorTime(loc.LastSeen); ok {
			metrics.HLNodeCritLocationLastSeenSeconds.WithLabelValues(file, line).Set(float64(t.Unix()))
		}
	}

	// Drop locations that dropped out of the current top-N.
	critLocationsMu.Lock()
	defer critLocationsMu.Unlock()
	for lbl := range activeLabels {
		if !current[lbl] {
			metrics.HLNodeCritLocation.DeleteLabelValues(lbl[0], lbl[1])
			metrics.HLNodeCritLocationLastSeenSeconds.DeleteLabelValues(lbl[0], lbl[1])
			delete(activeLabels, lbl)
		}
	}
	for lbl := range current {
		activeLabels[lbl] = true
	}
}
