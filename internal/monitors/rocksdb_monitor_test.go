package monitors

import (
	"os"
	"path/filepath"
	"testing"
)

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
		{"... usage: 172.18 MB ...", 180543815},  // 172.18 * 1024 * 1024 → 180543815 (truncated)
		{"... usage: 2.5 GB ...", 2684354560},    // 2.5 * 1024^3
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
	contents := `2026/05/25-11:12:23.794279 3528027 [db/db_impl/db_impl.cc:1084] ------- DUMPING STATS -------
... random unrelated text ...
Write Stall (count): l0-file-count-limit-stops: 1, total-stops: 1, Block cache LRUCache@0x1 capacity: 1.00 GB usage: 1.00 KB

2026/05/25-11:22:23.794672 3528027 [db/db_impl/db_impl.cc:1084] ------- DUMPING STATS -------
some more text
Write Stall (count): l0-file-count-limit-stops: 9, total-stops: 9, Block cache LRUCache@0x2 capacity: 2.00 GB usage: 5.00 MB

`
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
