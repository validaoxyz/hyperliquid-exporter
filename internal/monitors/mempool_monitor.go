package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// mempoolPollInterval — the file grows continuously; we offset-track so
// every tick reads only the new bytes. Tick fast enough that latency
// between mempool events and metric updates stays low without blocking
// the monitor goroutine on huge reads.
const mempoolPollInterval = 10 * time.Second

// mempoolEventAllowlist normalizes the on-disk event tags to safe Prometheus
// label values (strip spaces, lower-snake). New tags land in event_type="other"
// so cardinality is bounded regardless of hl-node version drift.
var mempoolEventAllowlist = map[string]string{
	"add_tx":                           "add_tx",
	"verify_block":                     "verify_block",
	"committed":                        "committed",
	"dropping blocks":                  "dropping_blocks",
	"register_block unknown tx hashes": "register_block_unknown_tx_hashes",
	"Size stats":                       "size_stats",
	"Pruned rpc request throttle":      "pruned_rpc_request_throttle",
}

// mempoolSizeComponents lists the per-component gauges we extract from
// "Size stats" event payloads.
var mempoolSizeComponents = map[string]bool{
	"committed_tx_hashes": true,
	"uncommitted_txs":     true,
	"blocks":              true,
	"rpc_requests":        true,
}

// StartMempoolMonitor watches $NODE_HOME/data/node_logs/mempool/hourly.
//
// The log is heavy (~24 MB/hour on testnet, larger on mainnet) — about
// 50 lines/sec dominated by add_tx and verify_block. We seek to EOF on
// startup (no historical replay), track file offset across ticks, and
// switch to the new hour file on rotation.
//
// Validator-only by default; many non-validator nodes do not write a
// mempool log.
//
// Per-event-type counters give per-second mempool activity. The "Size
// stats" event carries periodic mempool capacity snapshots we expose as
// gauges — committed_tx_hashes, uncommitted_txs, blocks, rpc_requests.
func StartMempoolMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "mempool", "hourly")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("mempool",
			"mempool directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("mempool", "watching %s", root)

	var currentFile string
	var currentOffset int64

	tick := func() {
		latest, err := latestHourlyFile(root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.DebugComponent("mempool", "find latest: %v", err)
			}
			return
		}
		if latest != currentFile {
			info, err := os.Stat(latest)
			if err != nil {
				return
			}
			logger.InfoComponent("mempool", "switched to %s (starting at EOF=%d)", latest, info.Size())
			currentFile = latest
			currentOffset = info.Size()
			return
		}
		newOffset, n, err := readMempoolEvents(currentFile, currentOffset)
		if err != nil {
			logger.DebugComponent("mempool", "read %s: %v", currentFile, err)
			return
		}
		currentOffset = newOffset
		if n > 0 {
			metrics.MarkMonitorTick("mempool")
		}
	}

	ticker := time.NewTicker(mempoolPollInterval)
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

// readMempoolEvents reads any new bytes appended to path since offset and
// increments per-event-type counters. "Size stats" lines also update the
// hl_mempool_size gauges. Returns the new file offset and the number of
// lines processed.
func readMempoolEvents(path string, offset int64) (int64, int, error) {
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
			processMempoolLine(line)
			offset += int64(len(line))
			processed++
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

// processMempoolLine parses one JSONL record and updates the relevant
// counters/gauges. Tolerates partial / malformed lines silently.
func processMempoolLine(line []byte) {
	if len(line) == 0 || line[0] != '[' {
		return
	}
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) < 1 {
		return
	}
	var tag string
	if err := json.Unmarshal(inner[0], &tag); err != nil {
		return
	}

	eventLabel, known := mempoolEventAllowlist[tag]
	if !known {
		eventLabel = "other"
	}

	// Status sub-label for the two events that carry an explicit outcome.
	status := ""
	switch tag {
	case "add_tx":
		// shape: ["add_tx", "<tx_hash>", <replace_bool>, "<status>"]
		if len(inner) >= 4 {
			_ = json.Unmarshal(inner[3], &status)
		}
	case "verify_block":
		// shape: ["verify_block", "<block_hash>", "<result>"]
		if len(inner) >= 3 {
			_ = json.Unmarshal(inner[2], &status)
		}
	}
	metrics.HLMempoolEventsTotal.WithLabelValues(eventLabel, status).Inc()

	// "Size stats" lines carry the capacity snapshot we want as gauges.
	if tag == "Size stats" && len(inner) >= 2 {
		// shape: ["Size stats", [["<component>", <n>], ...], ...]
		var pairs []json.RawMessage
		if err := json.Unmarshal(inner[1], &pairs); err == nil {
			for _, p := range pairs {
				var kv []json.RawMessage
				if err := json.Unmarshal(p, &kv); err != nil || len(kv) < 2 {
					continue
				}
				var key string
				var val float64
				if err := json.Unmarshal(kv[0], &key); err != nil {
					continue
				}
				if err := json.Unmarshal(kv[1], &val); err != nil {
					continue
				}
				if mempoolSizeComponents[key] {
					metrics.HLMempoolSize.WithLabelValues(key).Set(val)
				}
			}
		}
	}
}
