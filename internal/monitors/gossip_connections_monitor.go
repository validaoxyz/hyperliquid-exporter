package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// gossipConnectionEvents lists the event tag names the node emits to
// gossip_connections logs. The mapping was derived from a live mainnet
// peer's most recent hour-file. New tags appearing in the future will
// still be counted but will land in an "other" bucket so cardinality
// stays predictable.
//
// Operator semantics:
//   - rejecting_gossip_stream_max_peers_reached: capacity exhausted!
//   - error_checking_connection: peer churn / unreliable peers
//   - verified_gossip_rpc:       healthy handshake (counter rate = arrival rate)
var gossipConnectionAllowlist = map[string]string{
	"verified gossip rpc":                          "verified_gossip_rpc",
	"handle_stream_connection":                     "handle_stream_connection",
	"performing checks on stream":                  "performing_checks_on_stream",
	"got tcp greeting":                             "got_tcp_greeting",
	"error checking connection":                    "error_checking_connection",
	"rejecting gossip stream because max peers reached": "rejecting_gossip_stream_max_peers_reached",
}

// gossipConnectionsPollInterval is short because the file is append-only
// and operators want to see peer-rejection events promptly.
const gossipConnectionsPollInterval = 10 * time.Second

// StartGossipConnectionsMonitor watches
// $NODE_HOME/data/node_logs/gossip_connections/hourly. Each line is of
// the form ["<iso>", ["<event_tag>", {<event_specific_payload>}]]. We
// extract only the tag and count occurrences, keeping cardinality flat.
func StartGossipConnectionsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "gossip_connections", "hourly")

	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("gossip_connections",
			"gossip_connections directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("gossip_connections", "watching %s", root)

	var currentFile string
	var currentOffset int64

	tick := func() {
		latest, err := latestHourlyFile(root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.DebugComponent("gossip_connections", "find latest: %v", err)
			}
			return
		}
		if latest != currentFile {
			// On startup or rotation, jump to end so we don't replay history
			// (counters would otherwise spike on every restart).
			info, err := os.Stat(latest)
			if err != nil {
				return
			}
			logger.InfoComponent("gossip_connections", "switched to %s (starting at EOF=%d)", latest, info.Size())
			currentFile = latest
			currentOffset = info.Size()
			return
		}
		newOffset, n, err := readGossipConnectionEvents(currentFile, currentOffset)
		if err != nil {
			logger.DebugComponent("gossip_connections", "read %s: %v", currentFile, err)
			return
		}
		currentOffset = newOffset
		if n > 0 {
			metrics.MarkMonitorTick("gossip_connections")
		}
	}

	ticker := time.NewTicker(gossipConnectionsPollInterval)
	defer ticker.Stop()
	tick()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// readGossipConnectionEvents reads any new bytes appended to path since
// offset and increments the gauge counter for each parseable event. Returns
// the new file offset and the number of lines processed.
func readGossipConnectionEvents(path string, offset int64) (int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, 0, err
	}
	reader := bufio.NewReaderSize(f, 1<<20)
	processed := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if event, ok := parseGossipConnectionLine(line); ok {
				metrics.HLP2PGossipEventsTotal.WithLabelValues(event).Inc()
				processed++
			}
			offset += int64(len(line))
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return offset, processed, err
		}
	}
	return offset, processed, nil
}

// parseGossipConnectionLine returns the sanitized event-type label, or
// ok=false when the line is unparseable. Untracked tags are returned as
// "other" so total event volume is still visible.
func parseGossipConnectionLine(line []byte) (string, bool) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 || line[0] != '[' {
		return "", false
	}
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return "", false
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) < 1 {
		return "", false
	}
	var tag string
	if err := json.Unmarshal(inner[0], &tag); err != nil {
		return "", false
	}
	if name, known := gossipConnectionAllowlist[tag]; known {
		return name, true
	}
	return "other", true
}
