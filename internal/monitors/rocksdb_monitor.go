package monitors

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// rocksDBs is the allowlist of RocksDB directories under hyperliquid_data
// that we report on. These are the same databases the disk monitor
// reports sizes for; here we go further and parse the LOG file for
// LSM-tree health.
//
// `name` is the metric label; `relPath` is the path under cfg.NodeHome.
// We deliberately skip db_hub/Evm and db_hub/Exchange because they're
// memtable-only on this node (no SSTs, LOG never updates).
var rocksDBs = []struct {
	name    string
	relPath string
}{
	{"db_hub_rpc", "hyperliquid_data/db_hub/Rpc"},
	{"evm_db_fast", "hyperliquid_data/evm_db_hub_fast/EvmState"},
	{"evm_db_slow", "hyperliquid_data/evm_db_hub_slow/EvmState"},
}

const (
	rocksDBPollInterval = 60 * time.Second
	// rocksDBLogTailBytes is the tail window we read from each LOG file.
	// RocksDB writes a DUMPING STATS block roughly every 10 minutes, and
	// the block itself is ~5 KB. 256 KiB comfortably contains the latest
	// one even after many compaction events between stat dumps.
	rocksDBLogTailBytes = 256 * 1024
)

// StartRocksDBMonitor publishes per-database LSM-tree health metrics:
//
//   - hl_rocksdb_sst_files{db}              — total .sst file count on disk
//   - hl_rocksdb_write_stalls_total{db, reason} — write-stall counters
//     parsed from the LOG (modeled as gauges; resets on hl-node restart)
//   - hl_rocksdb_block_cache_usage_bytes{db} — current block-cache usage
//
// Write stalls are the textbook LSM-tree "falling behind" signal. A
// non-zero growth rate on `pending-compaction-bytes-stops` for the
// `db_hub_rpc` DB means RPC writes are blocked while waiting for
// compaction to catch up — the node will appear sluggish to clients
// while its disk is fine on the surface.
func StartRocksDBMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	logger.InfoComponent("rocksdb", "watching %d RocksDB instances", len(rocksDBs))

	ticker := time.NewTicker(rocksDBPollInterval)
	defer ticker.Stop()

	tickRocksDB(cfg.NodeHome)
	metrics.MarkMonitorTick("rocksdb")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickRocksDB(cfg.NodeHome)
			metrics.MarkMonitorTick("rocksdb")
		}
	}
}

func tickRocksDB(nodeHome string) {
	for _, db := range rocksDBs {
		dir := filepath.Join(nodeHome, db.relPath)
		if _, err := os.Stat(dir); err != nil {
			continue
		}

		metrics.HLRocksDBSSTFiles.WithLabelValues(db.name).Set(float64(countSSTFiles(dir)))

		logPath := filepath.Join(dir, "LOG")
		stats, ok := readRocksDBStats(logPath)
		if !ok {
			continue
		}
		for reason, count := range stats.writeStalls {
			metrics.HLRocksDBWriteStallsTotal.WithLabelValues(db.name, reason).Set(float64(count))
		}
		if stats.cacheUsageBytes >= 0 {
			metrics.HLRocksDBBlockCacheUsageBytes.WithLabelValues(db.name).Set(float64(stats.cacheUsageBytes))
		}
	}
}

func countSSTFiles(dir string) int {
	var n int
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".sst") {
			n++
		}
		return nil
	})
	return n
}

type rocksDBStats struct {
	writeStalls     map[string]int64
	cacheUsageBytes int64 // -1 if unparsed
}

// readRocksDBStats tail-reads a RocksDB LOG file, locates the LAST
// "DUMPING STATS" block, and extracts the write-stall counters and
// block cache usage. Returns ok=false if no stats block is found in
// the tail window or if the file can't be read.
func readRocksDBStats(path string) (rocksDBStats, bool) {
	stats := rocksDBStats{
		writeStalls:     map[string]int64{},
		cacheUsageBytes: -1,
	}

	f, err := os.Open(path)
	if err != nil {
		return stats, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return stats, false
	}
	start := info.Size() - rocksDBLogTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return stats, false
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.Read(buf); err != nil {
		return stats, false
	}

	// Locate the last DUMPING STATS marker.
	const marker = "------- DUMPING STATS -------"
	idx := strings.LastIndex(string(buf), marker)
	if idx < 0 {
		return stats, false
	}
	tail := string(buf[idx:])

	// The two signals we care about often appear on the SAME LINE in
	// real LOGs — RocksDB writes the long "Write Stall (count): ..."
	// line and tacks "Block cache LRUCache@... usage: X" onto the end.
	// Don't switch; check both predicates per line.
	for _, line := range strings.Split(tail, "\n") {
		if strings.Contains(line, "Write Stall (count):") {
			parseWriteStallLine(line, stats.writeStalls)
		}
		if strings.Contains(line, "Block cache LRUCache") {
			if v := parseBlockCacheUsage(line); v >= 0 {
				stats.cacheUsageBytes = v
			}
		}
	}
	if len(stats.writeStalls) == 0 && stats.cacheUsageBytes < 0 {
		return stats, false
	}
	return stats, true
}

// parseWriteStallLine parses tokens of the form `k: v` from a Write
// Stall line. Both the long combined line and the short
// write-buffer-manager line follow this shape, so we merge their keys
// into the same map.
//
//	Write Stall (count): cf-l0-file-count-limit-delays-with-ongoing-compaction: 0, ...
func parseWriteStallLine(line string, out map[string]int64) {
	// Strip the prefix up to and including the colon after "(count)".
	prefix := "Write Stall (count):"
	body := strings.TrimSpace(line[strings.Index(line, prefix)+len(prefix):])
	for _, part := range strings.Split(body, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Stop at the first token that isn't `key: value` — the long line
		// trails into "Block cache LRUCache@..." past the stall counters.
		colon := strings.Index(part, ":")
		if colon < 0 {
			break
		}
		key := strings.TrimSpace(part[:colon])
		val := strings.TrimSpace(part[colon+1:])
		// `val` may be just an integer, or it may be the start of a
		// non-stall token (Block cache...). Try to parse it as int; if
		// that fails, stop processing.
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			break
		}
		if isStallReason(key) {
			out[key] = n
		}
	}
}

// isStallReason filters the keys to the actual stall counters. Keeps
// the metric cardinality clean even if RocksDB sneaks new keys into
// the line in a future version.
func isStallReason(k string) bool {
	switch k {
	case "cf-l0-file-count-limit-delays-with-ongoing-compaction",
		"cf-l0-file-count-limit-stops-with-ongoing-compaction",
		"l0-file-count-limit-delays",
		"l0-file-count-limit-stops",
		"memtable-limit-delays",
		"memtable-limit-stops",
		"pending-compaction-bytes-delays",
		"pending-compaction-bytes-stops",
		"total-delays",
		"total-stops",
		"write-buffer-manager-limit-stops":
		return true
	}
	return false
}

// parseBlockCacheUsage extracts the `usage:` value from a Block cache
// line, e.g. "Block cache LRUCache@0x... capacity: 16.00 GB usage: 172.18 MB".
// Returns -1 on parse failure.
var cacheUsageRe = regexp.MustCompile(`usage:\s*([0-9.]+)\s*(KB|MB|GB|B)`)

func parseBlockCacheUsage(line string) int64 {
	m := cacheUsageRe.FindStringSubmatch(line)
	if len(m) < 3 {
		return -1
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return -1
	}
	switch m[2] {
	case "B":
		return int64(v)
	case "KB":
		return int64(v * 1024)
	case "MB":
		return int64(v * 1024 * 1024)
	case "GB":
		return int64(v * 1024 * 1024 * 1024)
	}
	return -1
}
