package monitors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func completeRocksDBLog(at time.Time, stops int64, cache string) string {
	parts := make([]string, 0, len(rocksDBStallReasons))
	for _, reason := range rocksDBStallReasons {
		value := int64(0)
		if reason == "l0-file-count-limit-stops" {
			value = stops
		}
		parts = append(parts, fmt.Sprintf("%s: %d", reason, value))
	}
	return fmt.Sprintf("%s 1 [db/db_impl/db_impl.cc:1084] ------- DUMPING STATS -------\nWrite Stall (count): %s, Block cache LRUCache@0x1 capacity: 2.00 GB usage: %s\n", at.Format("2006/01/02-15:04:05.000000"), strings.Join(parts, ", "), cache)
}

func reducedRocksDBLog(at time.Time, bufferManagerStops int64) string {
	return fmt.Sprintf(`%s 1 [db/db_impl/db_impl.cc:1084] ------- DUMPING STATS -------
** DB Stats **
Uptime(secs): 603.1 total, 600.0 interval
Cumulative writes: 5236 writes, 5236 keys
Cumulative WAL: 1 writes, 1 syncs
Cumulative stall: 00:00:0.000 H:M:S, 0.0 percent
Interval writes: 10 writes, 10 keys
Interval WAL: 1 writes, 1 syncs
Interval stall: 00:00:0.000 H:M:S, 0.0 percent
Write Stall (count): write-buffer-manager-limit-stops: %d,
`, at.Format("2006/01/02-15:04:05.000000"), bufferManagerStops)
}

func TestParseWriteStallLine(t *testing.T) {
	// Real line trimmed from a hl-node RocksDB LOG. Note the trailing
	// "Block cache" content — must NOT bleed into the stall counters.
	line := `Write Stall (count): cf-l0-file-count-limit-delays-with-ongoing-compaction: 0, cf-l0-file-count-limit-stops-with-ongoing-compaction: 0, l0-file-count-limit-delays: 0, l0-file-count-limit-stops: 5, memtable-limit-delays: 0, memtable-limit-stops: 0, pending-compaction-bytes-delays: 12, pending-compaction-bytes-stops: 0, total-delays: 12, total-stops: 5, Block cache LRUCache@0x7d capacity: 16.00 GB usage: 0.08 KB table_size: 1024 occupancy: 87`

	out := map[string]int64{}
	parseWriteStallLine(line, out)

	if out["l0-file-count-limit-stops"] != 5 {
		t.Errorf("got l0-file-count-limit-stops=%d want 5", out["l0-file-count-limit-stops"])
	}
	if out["pending-compaction-bytes-delays"] != 12 {
		t.Errorf("got pending-compaction-bytes-delays=%d want 12", out["pending-compaction-bytes-delays"])
	}
	if out["total-stops"] != 5 {
		t.Errorf("got total-stops=%d want 5", out["total-stops"])
	}
	if _, present := out["Block cache LRUCache@0x7d capacity"]; present {
		t.Errorf("Block cache token should not be counted as a stall reason")
	}
	if _, present := out["table_size"]; present {
		t.Errorf("table_size token should not be counted as a stall reason")
	}
}

func TestParseBlockCacheUsage(t *testing.T) {
	cases := []struct {
		line string
		want int64
	}{
		{"Block cache LRUCache@0x... capacity: 16.00 GB usage: 0.08 KB table_size: 1024", 81}, // 0.08 * 1024 → 81 (truncated)
		{"... usage: 172.18 MB ...", 180543815},                                               // 172.18 * 1024 * 1024 → 180543815 (truncated)
		{"... usage: 2.5 GB ...", 2684354560},                                                 // 2.5 * 1024^3
		{"... usage: 4096 B ...", 4096},
		{"no usage here", -1},
	}
	for _, c := range cases {
		if got := parseBlockCacheUsage(c.line); got != c.want {
			t.Errorf("line %q: got %d want %d", c.line, got, c.want)
		}
	}
}

func TestReadRocksDBStats_RealSample(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	// Two DUMPING STATS blocks. The reader should pick the LAST one
	// (the one with higher counters), not the first.
	contents := completeRocksDBLog(time.Date(2026, 5, 25, 11, 12, 23, 794279000, time.UTC), 1, "1.00 KB") +
		completeRocksDBLog(time.Date(2026, 5, 25, 11, 22, 23, 794672000, time.UTC), 9, "5.00 MB")
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, ok := readRocksDBStats(logPath)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if stats.writeStalls["l0-file-count-limit-stops"] != 9 {
		t.Errorf("expected the LAST stats block (=9), got %d", stats.writeStalls["l0-file-count-limit-stops"])
	}
	wantCache := int64(5 * 1024 * 1024)
	if stats.cacheUsageBytes != wantCache {
		t.Errorf("cache usage: got %d want %d", stats.cacheUsageBytes, wantCache)
	}
}

func TestReadRocksDBStatsAcceptsReducedLiveProjection(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	at := time.Date(2026, 8, 9, 3, 4, 5, 6000, time.UTC)
	contents := reducedRocksDBLog(at, 7) + reducedRocksDBLog(at.Add(10*time.Minute), 9)
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := readRocksDBStatsComplete(logPath)
	if err != nil {
		t.Fatalf("reduced live projection rejected: %v", err)
	}
	if got := stats.writeStalls["write-buffer-manager-limit-stops"]; got != 7 {
		t.Fatalf("latest marker-delimited write-buffer-manager stalls = %d, want 7", got)
	}
	if len(stats.writeStalls) != 1 {
		t.Fatalf("reduced projection published %d stall reasons, want 1", len(stats.writeStalls))
	}
	if stats.cacheUsageBytes != -1 {
		t.Fatalf("missing cache usage = %d, want unavailable", stats.cacheUsageBytes)
	}
}

func TestReadRocksDBStatsDoesNotCommitInProgressFullProjection(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	at := time.Date(2026, 8, 9, 3, 4, 5, 6000, time.UTC)
	partial := fmt.Sprintf("%s 1 [x] ------- DUMPING STATS -------\nWrite Stall (count): write-buffer-manager-limit-stops: 99\n", at.Add(10*time.Minute).Format("2006/01/02-15:04:05.000000"))
	if err := os.WriteFile(logPath, []byte(completeRocksDBLog(at, 3, "4.00 MB")+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := readRocksDBStatsComplete(logPath)
	if err != nil {
		t.Fatalf("fall back to marker-delimited full projection: %v", err)
	}
	if got := stats.writeStalls["l0-file-count-limit-stops"]; got != 3 {
		t.Fatalf("in-progress dump replaced prior full stalls: got %d want 3", got)
	}
	if got := stats.writeStalls["write-buffer-manager-limit-stops"]; got != 0 {
		t.Fatalf("in-progress dump leaked write-buffer stalls: got %d want 0", got)
	}
	if want := int64(4 * 1024 * 1024); stats.cacheUsageBytes != want {
		t.Fatalf("in-progress dump deleted prior cache: got %d want %d", stats.cacheUsageBytes, want)
	}
}

func TestReadRocksDBStatsFallsBackBeforeNewestMetricsArrive(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	at := time.Date(2026, 8, 9, 3, 4, 5, 6000, time.UTC)
	partial := fmt.Sprintf("%s 1 [x] ------- DUMPING STATS -------\n** DB Stats **\n", at.Add(10*time.Minute).Format("2006/01/02-15:04:05.000000"))
	if err := os.WriteFile(logPath, []byte(completeRocksDBLog(at, 3, "4.00 MB")+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := readRocksDBStatsComplete(logPath)
	if err != nil {
		t.Fatalf("fall back before newest metrics arrive: %v", err)
	}
	if got := stats.writeStalls["l0-file-count-limit-stops"]; got != 3 {
		t.Fatalf("early newest dump replaced prior full stalls: got %d want 3", got)
	}
	if want := int64(4 * 1024 * 1024); stats.cacheUsageBytes != want {
		t.Fatalf("early newest dump deleted prior cache: got %d want %d", stats.cacheUsageBytes, want)
	}
}

func TestReadRocksDBStatsFallsBackWhileNewestMetricLineIsTorn(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	at := time.Date(2026, 8, 9, 3, 4, 5, 6000, time.UTC)
	partial := fmt.Sprintf("%s 1 [x] ------- DUMPING STATS -------\nWrite Stall (count): write-buffer-manager-limit-stops:", at.Add(10*time.Minute).Format("2006/01/02-15:04:05.000000"))
	if err := os.WriteFile(logPath, []byte(completeRocksDBLog(at, 3, "4.00 MB")+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := readRocksDBStatsComplete(logPath)
	if err != nil {
		t.Fatalf("fall back while newest metric line is torn: %v", err)
	}
	if got := stats.writeStalls["l0-file-count-limit-stops"]; got != 3 {
		t.Fatalf("torn newest dump replaced prior full stalls: got %d want 3", got)
	}
	if want := int64(4 * 1024 * 1024); stats.cacheUsageBytes != want {
		t.Fatalf("torn newest dump deleted prior cache: got %d want %d", stats.cacheUsageBytes, want)
	}

	// Once another marker closes that malformed dump, it is a completed bad
	// generation and must fail instead of falling back through it.
	contents := completeRocksDBLog(at, 3, "4.00 MB") + partial + "\n" + reducedRocksDBLog(at.Add(20*time.Minute), 0)
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRocksDBStatsComplete(logPath); err == nil {
		t.Fatal("marker-delimited malformed projection was accepted")
	}
}

func TestReadRocksDBStatsRejectsUnknownMarkerDelimitedProjections(t *testing.T) {
	at := time.Date(2026, 8, 9, 3, 4, 5, 6000, time.UTC)
	marker := func(offset time.Duration) string {
		return fmt.Sprintf("%s 1 [x] ------- DUMPING STATS -------\n", at.Add(offset).Format("2006/01/02-15:04:05.000000"))
	}
	cases := map[string]string{
		"arbitrary stall subset": marker(0) + "Write Stall (count): l0-file-count-limit-stops: 1\n" + marker(time.Minute),
		"cache only":             marker(0) + "Block cache LRUCache@0x1 usage: 1.00 MB\n" + marker(time.Minute),
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "LOG")
			if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readRocksDBStatsComplete(logPath); err == nil {
				t.Fatal("unsupported marker-delimited projection was accepted")
			}
		})
	}
}

func TestRocksDBCompleteSnapshotRollbackAbsenceAndRecovery(t *testing.T) {
	nodeHome := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(nodeHome, "hyperliquid_data", "db_hub", "Rpc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.sst"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "LOG")
	if err := os.WriteFile(logPath, []byte(completeRocksDBLog(now, 3, "4.00 MB")), 0o644); err != nil {
		t.Fatal(err)
	}
	state := newRocksDBMonitorState()
	tickRocksDB(nodeHome, state, now.Add(time.Second))
	labels := map[string]string{"db": "db_hub_rpc", "reason": "l0-file-count-limit-stops"}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBWriteStallsTotal, labels); !ok || value != 3 {
		t.Fatalf("complete rocksdb snapshot = %v, %v", value, ok)
	}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBSSTFiles, map[string]string{"db": "db_hub_rpc"}); !ok || value != 1 {
		t.Fatalf("sst count = %v, %v", value, ok)
	}

	// A syntactically present block with a malformed recognized value retains
	// every prior child.
	if err := os.WriteFile(logPath, []byte(fmt.Sprintf("%s 1 [x] ------- DUMPING STATS -------\nWrite Stall (count): l0-file-count-limit-stops: nope\n", now.Add(time.Minute).Format("2006/01/02-15:04:05.000000"))), 0o644); err != nil {
		t.Fatal(err)
	}
	tickRocksDB(nodeHome, state, now.Add(2*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBWriteStallsTotal, labels); !ok || value != 3 {
		t.Fatalf("incomplete block replaced last good = %v, %v", value, ok)
	}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBStatsParseOK, map[string]string{"db": "db_hub_rpc"}); !ok || value != 0 {
		t.Fatalf("malformed parse state = %v, %v", value, ok)
	}
	failedLastValid := state.dbs["db_hub_rpc"].lastValid
	// Re-validating the same retained stats generation is still a successful
	// per-DB recovery and must refresh its last-valid receipt time. The common
	// source observation timestamp remains tied to actual sample advancement.
	if err := os.WriteFile(logPath, []byte(completeRocksDBLog(now, 3, "4.00 MB")), 0o644); err != nil {
		t.Fatal(err)
	}
	tickRocksDB(nodeHome, state, now.Add(2500*time.Millisecond))
	if !state.dbs["db_hub_rpc"].lastValid.After(failedLastValid) {
		t.Fatal("RocksDB same-generation recovery did not refresh per-DB last-valid time")
	}

	// Present-empty is distinct from absent and still retains last good data.
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tickRocksDB(nodeHome, state, now.Add(3*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBSourcePresent, map[string]string{"db": "db_hub_rpc"}); !ok || value != 1 {
		t.Fatalf("empty LOG presence = %v, %v", value, ok)
	}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBWriteStallsTotal, labels); !ok || value != 3 {
		t.Fatal("empty LOG cleared last complete stats")
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	tickRocksDB(nodeHome, state, now.Add(4*time.Second))
	if b03CollectorHasLabels(t, metrics.HLRocksDBWriteStallsTotal, labels) {
		t.Fatal("absent RocksDB LOG retained current stats")
	}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBSourcePresent, map[string]string{"db": "db_hub_rpc"}); !ok || value != 0 {
		t.Fatalf("absent LOG presence = %v, %v", value, ok)
	}

	if err := os.WriteFile(logPath, []byte(completeRocksDBLog(now.Add(5*time.Second), 7, "8.00 MB")), 0o644); err != nil {
		t.Fatal(err)
	}
	tickRocksDB(nodeHome, state, now.Add(6*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBWriteStallsTotal, labels); !ok || value != 7 {
		t.Fatalf("RocksDB recovery = %v, %v", value, ok)
	}

	// A complete reduced projection replaces the available-field set. It must
	// not preserve omitted per-CF/cache values from the prior full projection.
	contents := reducedRocksDBLog(now.Add(7*time.Second), 11) + reducedRocksDBLog(now.Add(8*time.Second), 13)
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	tickRocksDB(nodeHome, state, now.Add(8*time.Second))
	bufferLabels := map[string]string{"db": "db_hub_rpc", "reason": "write-buffer-manager-limit-stops"}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBWriteStallsTotal, bufferLabels); !ok || value != 11 {
		t.Fatalf("reduced write-buffer metric = %v, %v", value, ok)
	}
	if b03CollectorHasLabels(t, metrics.HLRocksDBWriteStallsTotal, labels) {
		t.Fatal("reduced projection retained omitted per-CF stall value")
	}
	if b03CollectorHasLabels(t, metrics.HLRocksDBBlockCacheUsageBytes, map[string]string{"db": "db_hub_rpc"}) {
		t.Fatal("reduced projection retained omitted cache value")
	}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBSSTFiles, map[string]string{"db": "db_hub_rpc"}); !ok || value != 1 {
		t.Fatalf("reduced projection SST count = %v, %v", value, ok)
	}
	if value, ok := b03CollectorValue(t, metrics.HLRocksDBStatsParseOK, map[string]string{"db": "db_hub_rpc"}); !ok || value != 1 {
		t.Fatalf("reduced projection parse state = %v, %v", value, ok)
	}
}

func TestReadRocksDBStatsRejectsMalformedOptionalCache(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	at := time.Date(2026, 8, 9, 3, 4, 5, 6000, time.UTC)
	contents := fmt.Sprintf("%s 1 [x] ------- DUMPING STATS -------\nBlock cache LRUCache@0x1 usage: nope MB\n", at.Format("2006/01/02-15:04:05.000000"))
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRocksDBStatsComplete(logPath); err == nil {
		t.Fatal("malformed present cache usage was treated as unavailable")
	}
}

func TestReadRocksDBStats_NoStatsBlock(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "LOG")
	// Real startup-log content with NO stats block yet.
	if err := os.WriteFile(logPath, []byte("DB pointer 0x...\nrecovery_started\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok := readRocksDBStats(logPath)
	if ok {
		t.Errorf("expected ok=false when no DUMPING STATS marker present")
	}
}

func TestCountSSTFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"foo.sst", "bar.sst", "baz.log", "qux.ldb", "subdir/inner.sst"} {
		path := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := countSSTFiles(dir); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}
