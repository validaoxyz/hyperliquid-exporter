//go:build linux

package monitors

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeProcNetTCP is a minimal /proc/net/tcp file with one row per state
// we care about, all on hl-node's listening ports. Header line is the
// real kernel format.
const fakeProcNetTCPSample = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0FA1 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:0FA2 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:0FA1 0200007F:CD00 01 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:0FA1 0200007F:CD01 01 00000000:00000000 00:00000000 00000000     0        0 12348 1 0000000000000000 100 0 0 10 0
   4: 0100007F:0FA1 0200007F:CD02 06 00000000:00000000 00:00000000 00000000     0        0 12349 1 0000000000000000 100 0 0 10 0
   5: 0100007F:0FA2 0200007F:CD03 06 00000000:00000000 00:00000000 00000000     0        0 12350 1 0000000000000000 100 0 0 10 0
   6: 0100007F:0FA2 0200007F:CD04 06 00000000:00000000 00:00000000 00000000     0        0 12351 1 0000000000000000 100 0 0 10 0
   7: 0100007F:1234 0200007F:CD05 01 00000000:00000000 00:00000000 00000000     0        0 12352 1 0000000000000000 100 0 0 10 0
`

// readTCPFile is the helper inside tickTCPConnections that actually
// parses; expose a small test wrapper here that uses a temp file.
func TestReadTCPFile_StateCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	if err := os.WriteFile(path, []byte(fakeProcNetTCPSample), 0o644); err != nil {
		t.Fatal(err)
	}

	counts := map[uint16]map[string]int{
		4001: {},
		4002: {},
		3001: {},
		3999: {},
	}
	readTCPFile(path, counts)

	// Expectations from the fixture:
	//   4001: 1 LISTEN (row 0) + 2 ESTABLISHED (rows 2,3) + 1 TIME_WAIT (row 4)
	//   4002: 1 LISTEN (row 1) + 2 TIME_WAIT (rows 5,6)
	//   row 7 (port 0x1234) is not in our allowlist; ignored.
	if got := counts[4001]["LISTEN"]; got != 1 {
		t.Errorf("4001 LISTEN: got %d want 1", got)
	}
	if got := counts[4001]["ESTABLISHED"]; got != 2 {
		t.Errorf("4001 ESTABLISHED: got %d want 2", got)
	}
	if got := counts[4001]["TIME_WAIT"]; got != 1 {
		t.Errorf("4001 TIME_WAIT: got %d want 1", got)
	}
	if got := counts[4002]["LISTEN"]; got != 1 {
		t.Errorf("4002 LISTEN: got %d want 1", got)
	}
	if got := counts[4002]["TIME_WAIT"]; got != 2 {
		t.Errorf("4002 TIME_WAIT: got %d want 2", got)
	}
	if got := counts[3001]["LISTEN"]; got != 0 {
		t.Errorf("3001 LISTEN: got %d want 0", got)
	}
}

func TestReadTCPFile_IgnoresMalformedRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	// First line is the header (skipped), then one valid row, then a row
	// with too few fields, then one with non-hex state.
	contents := `  sl header header header header etc
   0: 00000000:0FA1 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0
   1: junk
   2: 00000000:0FA1 00000000:0000 ZZ 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	counts := map[uint16]map[string]int{4001: {}}
	readTCPFile(path, counts)
	if got := counts[4001]["LISTEN"]; got != 1 {
		t.Errorf("expected 1 LISTEN from the valid row, got %d", got)
	}
}

func TestReadTCPFile_MissingFile(t *testing.T) {
	counts := map[uint16]map[string]int{4001: {}}
	readTCPFile("/nonexistent/path", counts)
	if len(counts[4001]) != 0 {
		t.Errorf("expected no entries when file missing, got %v", counts[4001])
	}
}
