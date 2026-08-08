package monitors

import (
	"context"
	"errors"
	"fmt"
	"io"
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

var rocksDBs = []struct {
	name    string
	relPath string
}{
	{"db_hub_rpc", "hyperliquid_data/db_hub/Rpc"},
	{"evm_db_fast", "hyperliquid_data/evm_db_hub_fast/EvmState"},
	{"evm_db_slow", "hyperliquid_data/evm_db_hub_slow/EvmState"},
}

var rocksDBStallReasons = []string{
	"cf-l0-file-count-limit-delays-with-ongoing-compaction",
	"cf-l0-file-count-limit-stops-with-ongoing-compaction",
	"l0-file-count-limit-delays",
	"l0-file-count-limit-stops",
	"memtable-limit-delays",
	"memtable-limit-stops",
	"pending-compaction-bytes-delays",
	"pending-compaction-bytes-stops",
	"total-delays",
	"total-stops",
	"write-buffer-manager-limit-stops",
}

const (
	rocksDBPollInterval = 60 * time.Second
	rocksDBLogTailBytes = 256 * 1024
)

var errRocksDBStatsNoRecognizedMetrics = errors.New("stats block contains no recognized metrics")

type rocksDBStats struct {
	writeStalls     map[string]int64
	cacheUsageBytes int64
	sampleTime      time.Time
}

type rocksDBInstanceState struct {
	published      bool
	lastValid      time.Time
	lastSampleTime time.Time
}

type rocksDBMonitorState struct {
	dbs map[string]*rocksDBInstanceState
}

type rocksDBTickDecision struct {
	name          string
	instance      *rocksDBInstanceState
	setPresent    bool
	present       float64
	parseOK       float64
	withdraw      bool
	publish       bool
	sstFiles      int
	stats         rocksDBStats
	lastValid     time.Time
	sampleChanged bool
}

func newRocksDBMonitorState() *rocksDBMonitorState {
	state := &rocksDBMonitorState{dbs: make(map[string]*rocksDBInstanceState, len(rocksDBs))}
	for _, db := range rocksDBs {
		state.dbs[db.name] = &rocksDBInstanceState{}
	}
	return state
}

func StartRocksDBMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	logger.InfoComponent("rocksdb", "watching %d RocksDB instances", len(rocksDBs))
	metrics.RegisterSource(metrics.SourceRocksDB, true)
	state := newRocksDBMonitorState()

	ticker := time.NewTicker(rocksDBPollInterval)
	defer ticker.Stop()
	tickRocksDB(cfg.NodeHome, state, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickRocksDB(cfg.NodeHome, state, time.Now())
		}
	}
}

func tickRocksDB(nodeHome string, state *rocksDBMonitorState, now time.Time) {
	metrics.MarkMonitorAttempt("rocksdb")
	metrics.MarkSourceAttempt(metrics.SourceRocksDB)
	decisions := make([]rocksDBTickDecision, 0, len(rocksDBs))
	validNew := false
	anyPresent := false
	uncertainPresence := false
	var failureStage metrics.SourceFailureStage
	recordFailure := func(stage metrics.SourceFailureStage) {
		// Preserve the strongest I/O diagnosis for the aggregate bounded
		// source while per-DB parse state retains the exact local outcome.
		if failureStage == "" || rocksDBFailurePriority(stage) > rocksDBFailurePriority(failureStage) {
			failureStage = stage
		}
	}
	var newest time.Time
	for _, db := range rocksDBs {
		instance := state.dbs[db.name]
		decision := rocksDBTickDecision{
			name:      db.name,
			instance:  instance,
			parseOK:   -1,
			lastValid: instance.lastValid,
		}
		dir := filepath.Join(nodeHome, db.relPath)
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				decision.withdraw = true
				decision.setPresent = true
				decision.present = 0
			} else {
				uncertainPresence = true
				recordFailure(metrics.SourceFailureStat)
			}
			decisions = append(decisions, decision)
			continue
		}
		logPath := filepath.Join(dir, "LOG")
		if _, err := os.Stat(logPath); err != nil {
			if os.IsNotExist(err) {
				decision.withdraw = true
				decision.setPresent = true
				decision.present = 0
			} else {
				uncertainPresence = true
				recordFailure(metrics.SourceFailureStat)
			}
			decisions = append(decisions, decision)
			continue
		}
		anyPresent = true
		decision.setPresent = true
		decision.present = 1

		sstFiles, err := countSSTFilesComplete(dir)
		if err != nil {
			recordFailure(metrics.SourceFailureWalk)
			decisions = append(decisions, decision)
			continue
		}
		stats, err := readRocksDBStatsComplete(logPath)
		if err != nil {
			stage := rocksDBStatsFailureStage(err)
			recordFailure(stage)
			parseState := 0.0
			if stage != metrics.SourceFailureSchema {
				parseState = -1
			}
			decision.parseOK = parseState
			decisions = append(decisions, decision)
			continue
		}

		decision.publish = true
		decision.sstFiles = sstFiles
		decision.stats = stats
		decision.parseOK = 1
		decision.lastValid = now
		if !stats.sampleTime.Equal(instance.lastSampleTime) {
			decision.sampleChanged = true
			validNew = true
			if stats.sampleTime.After(newest) {
				newest = stats.sampleTime
			}
		}
		decisions = append(decisions, decision)
	}

	metrics.WithPrometheusSnapshotUpdate(func() {
		for _, decision := range decisions {
			if decision.withdraw {
				withdrawRocksDBSnapshot(decision.name, decision.instance)
			}
			if decision.publish {
				publishRocksDBSnapshot(decision.name, decision.sstFiles, decision.stats)
				decision.instance.lastValid = decision.lastValid
				if decision.sampleChanged {
					decision.instance.lastSampleTime = decision.stats.sampleTime
				}
				decision.instance.published = true
				metrics.HLRocksDBStatsLastValidTimestamp.WithLabelValues(decision.name).Set(float64(now.Unix()))
			}
			if decision.setPresent {
				metrics.HLRocksDBSourcePresent.WithLabelValues(decision.name).Set(decision.present)
			}
			metrics.HLRocksDBStatsParseOK.WithLabelValues(decision.name).Set(decision.parseOK)
			metrics.SetRocksDBLastValidAge(decision.name, decision.instance.lastValid, now)
		}

		if validNew {
			metrics.MarkSourceValidObservation(metrics.SourceRocksDB, newest)
			metrics.MarkMonitorValidObservation("rocksdb")
		}
		if anyPresent {
			metrics.MarkSourcePublication(metrics.SourceRocksDB)
			metrics.MarkMonitorPublication("rocksdb")
		} else if !uncertainPresence {
			metrics.MarkSourceAbsent(metrics.SourceRocksDB)
		}
		if failureStage != "" {
			metrics.MarkSourceError(metrics.SourceRocksDB, failureStage)
		}
	})
}

func rocksDBFailurePriority(stage metrics.SourceFailureStage) int {
	switch stage {
	case metrics.SourceFailureStat, metrics.SourceFailureOpen, metrics.SourceFailureRead, metrics.SourceFailureWalk:
		return 2
	case metrics.SourceFailureDecode, metrics.SourceFailureSchema:
		return 1
	default:
		return 0
	}
}

func rocksDBStatsFailureStage(err error) metrics.SourceFailureStage {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return metrics.SourceFailureRead
	}
	return metrics.SourceFailureSchema
}

func publishRocksDBSnapshot(db string, sstFiles int, stats rocksDBStats) {
	metrics.HLRocksDBSSTFiles.WithLabelValues(db).Set(float64(sstFiles))
	for _, reason := range rocksDBStallReasons {
		value, available := stats.writeStalls[reason]
		if !available {
			metrics.HLRocksDBWriteStallsTotal.DeleteLabelValues(db, reason)
			continue
		}
		metrics.HLRocksDBWriteStallsTotal.WithLabelValues(db, reason).Set(float64(value))
	}
	if stats.cacheUsageBytes >= 0 {
		metrics.HLRocksDBBlockCacheUsageBytes.WithLabelValues(db).Set(float64(stats.cacheUsageBytes))
	} else {
		metrics.HLRocksDBBlockCacheUsageBytes.DeleteLabelValues(db)
	}
}

func withdrawRocksDBSnapshot(db string, state *rocksDBInstanceState) {
	if !state.published {
		return
	}
	metrics.HLRocksDBSSTFiles.DeleteLabelValues(db)
	for _, reason := range rocksDBStallReasons {
		metrics.HLRocksDBWriteStallsTotal.DeleteLabelValues(db, reason)
	}
	metrics.HLRocksDBBlockCacheUsageBytes.DeleteLabelValues(db)
	state.published = false
}

func countSSTFiles(dir string) int {
	n, _ := countSSTFilesComplete(dir)
	return n
}

func countSSTFilesComplete(dir string) (int, error) {
	var n int
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sst") {
			n++
		}
		return nil
	})
	return n, err
}

func readRocksDBStats(path string) (rocksDBStats, bool) {
	stats, err := readRocksDBStatsComplete(path)
	return stats, err == nil
}

func readRocksDBStatsComplete(path string) (rocksDBStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return rocksDBStats{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return rocksDBStats{}, err
	}
	start := info.Size() - rocksDBLogTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return rocksDBStats{}, err
	}
	buf := make([]byte, info.Size()-start)
	if _, err := io.ReadFull(f, buf); err != nil {
		return rocksDBStats{}, err
	}
	const marker = "------- DUMPING STATS -------"
	text := string(buf)
	latestMarker := strings.LastIndex(text, marker)
	if latestMarker < 0 {
		return rocksDBStats{}, fmt.Errorf("no complete stats marker in tail")
	}
	latestStart := strings.LastIndex(text[:latestMarker], "\n") + 1
	latest, latestErr := parseRocksDBStatsBlock(text[latestStart:])
	if latestErr == nil && rocksDBStatsHasFullProjection(latest) {
		return latest, nil
	}

	// A marker may be visible before the first metric line is appended. That
	// is an incomplete newest dump, not a reason to reject the immediately
	// preceding marker-delimited generation.
	previousMarker := strings.LastIndex(text[:latestMarker], marker)
	if previousMarker < 0 {
		if latestErr != nil {
			return rocksDBStats{}, latestErr
		}
		return rocksDBStats{}, fmt.Errorf("reduced stats projection has no following marker")
	}
	previousStart := strings.LastIndex(text[:previousMarker], "\n") + 1
	previous, err := parseRocksDBStatsBlock(text[previousStart:latestStart])
	if err != nil {
		return rocksDBStats{}, err
	}
	if !rocksDBStatsHasKnownProjection(previous) {
		return rocksDBStats{}, fmt.Errorf("unsupported marker-delimited stats projection")
	}

	// A full projection proves its own completeness because every known stall
	// reason and cache usage arrive in the same dump. A reduced projection has
	// no terminal sentinel, so accepting the last one at EOF can transiently
	// publish an in-progress full dump. Require the following marker to close a
	// reduced projection, accepting the immediately preceding block instead.
	return previous, nil
}

func parseRocksDBStatsBlock(block string) (rocksDBStats, error) {
	const marker = "------- DUMPING STATS -------"
	idx := strings.Index(block, marker)
	if idx < 0 {
		return rocksDBStats{}, fmt.Errorf("missing stats marker")
	}
	lineStart := strings.LastIndex(block[:idx], "\n") + 1
	markerLineEnd := strings.IndexByte(block[idx:], '\n')
	if markerLineEnd < 0 {
		return rocksDBStats{}, fmt.Errorf("unterminated stats marker")
	}
	markerLine := block[lineStart : idx+markerLineEnd]
	sampleTime, err := parseRocksDBLogTime(markerLine)
	if err != nil {
		return rocksDBStats{}, err
	}
	stats := rocksDBStats{writeStalls: make(map[string]int64), cacheUsageBytes: -1, sampleTime: sampleTime}
	for _, line := range strings.Split(block[idx:], "\n") {
		if strings.Contains(line, "Write Stall (count):") {
			if err := parseWriteStallLineStrict(line, stats.writeStalls); err != nil {
				return rocksDBStats{}, err
			}
		}
		if strings.Contains(line, "Block cache LRUCache") {
			value := parseBlockCacheUsage(line)
			if value < 0 {
				return rocksDBStats{}, fmt.Errorf("invalid block-cache usage")
			}
			stats.cacheUsageBytes = value
		}
	}
	// RocksDB emits different complete stats projections for different DB
	// configurations. Some current instances expose only the global write-
	// buffer-manager stall reason and no block cache. Missing optional fields
	// withdraw their old series; a block with no recognized metric remains
	// unusable rather than fabricating an all-zero snapshot.
	if len(stats.writeStalls) == 0 && stats.cacheUsageBytes < 0 {
		return rocksDBStats{}, errRocksDBStatsNoRecognizedMetrics
	}
	return stats, nil
}

func rocksDBStatsHasFullProjection(stats rocksDBStats) bool {
	if stats.cacheUsageBytes < 0 {
		return false
	}
	for _, reason := range rocksDBStallReasons {
		if _, available := stats.writeStalls[reason]; !available {
			return false
		}
	}
	return true
}

func rocksDBStatsHasReducedProjection(stats rocksDBStats) bool {
	if stats.cacheUsageBytes >= 0 || len(stats.writeStalls) != 1 {
		return false
	}
	_, available := stats.writeStalls["write-buffer-manager-limit-stops"]
	return available
}

func rocksDBStatsHasKnownProjection(stats rocksDBStats) bool {
	return rocksDBStatsHasFullProjection(stats) || rocksDBStatsHasReducedProjection(stats)
}

func parseWriteStallLine(line string, out map[string]int64) {
	_ = parseWriteStallLineStrict(line, out)
}

func parseWriteStallLineStrict(line string, out map[string]int64) error {
	prefix := "Write Stall (count):"
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return fmt.Errorf("missing write-stall prefix")
	}
	body := strings.TrimSpace(line[idx+len(prefix):])
	for _, part := range strings.Split(body, ",") {
		part = strings.TrimSpace(part)
		colon := strings.Index(part, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(part[:colon])
		if !isStallReason(key) {
			continue
		}
		value := strings.TrimSpace(part[colon+1:])
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid write-stall value for %s", key)
		}
		out[key] = n
	}
	return nil
}

func isStallReason(key string) bool {
	for _, reason := range rocksDBStallReasons {
		if key == reason {
			return true
		}
	}
	return false
}

var (
	cacheUsageRe     = regexp.MustCompile(`usage:\s*([0-9.]+)\s*(KB|MB|GB|TB|B)`)
	rocksDBLogTimeRe = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2}-\d{2}:\d{2}:\d{2}\.\d+)`)
)

func parseBlockCacheUsage(line string) int64 {
	match := cacheUsageRe.FindStringSubmatch(line)
	if len(match) < 3 {
		return -1
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil || value < 0 {
		return -1
	}
	multiplier := float64(1)
	switch match[2] {
	case "KB":
		multiplier = 1024
	case "MB":
		multiplier = 1024 * 1024
	case "GB":
		multiplier = 1024 * 1024 * 1024
	case "TB":
		multiplier = 1024 * 1024 * 1024 * 1024
	}
	return int64(value * multiplier)
}

func parseRocksDBLogTime(line string) (time.Time, error) {
	match := rocksDBLogTimeRe.FindStringSubmatch(line)
	if len(match) != 2 {
		return time.Time{}, fmt.Errorf("missing stats-block timestamp")
	}
	t, err := time.Parse("2006/01/02-15:04:05.999999", match[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stats-block timestamp: %w", err)
	}
	return t, nil
}
