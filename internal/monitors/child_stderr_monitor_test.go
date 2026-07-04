package monitors

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyChildStderr(t *testing.T) {
	cases := map[string]string{
		`computed app hash 0xabc does not match quorum value QuorumAppHash{...}. Saved to /tmp/abci_state_bad_app_hash.rmp`: "app_hash_mismatch",
		`observed qc for newer hardfork, crashing. current=(1180,616516191)`:                                                "hardfork_upgrade",
		`thread 'main' panicked: too many blocks to request in gossip forward_client_blocks`:                                "sync_overflow",
		`CONFIGURATION ERROR: validator is not in node_ips: [...]`:                                                          "config_error",
		`could not read /home/ubuntu/hl/override_public_ip_address`:                                                         "config_error",
		`upstream connect error or disconnect/reset before headers`:                                                         "network",
		`thread 'main' panicked at 'attempt to add with overflow'`:                                                          "panic",
	}
	for input, want := range cases {
		if got := classifyChildStderr([]byte(input)); got != want {
			t.Errorf("classify(%.40q) = %q, want %q", input, got, want)
		}
	}
}

func TestTickChildStderr(t *testing.T) {
	root := t.TempDir()
	// note: date dir deliberately future-dated relative to nothing; the
	// monitor must key off mtimes, not names
	mkfile := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile("20270724/0/2026-07-04T07:08:31Z", "") // clean start
	mkfile("20270724/1/2026-07-04T08:00:00Z", "computed app hash x does not match quorum value")
	mkfile("20260101/0/2026-01-01T00:00:00Z", "too many blocks to request")

	seen := map[string]*childStderrState{}
	tickChildStderr(root, seen)

	if len(seen) != 3 {
		t.Fatalf("seen = %d files, want 3", len(seen))
	}
	reasons := map[string]int{}
	for _, st := range seen {
		reasons[st.reason]++
	}
	if reasons[""] != 1 || reasons["app_hash_mismatch"] != 1 || reasons["sync_overflow"] != 1 {
		t.Fatalf("classification off: %v", reasons)
	}

	// pruning: removing a file drops it from the cache next tick
	if err := os.RemoveAll(filepath.Join(root, "20260101")); err != nil {
		t.Fatal(err)
	}
	tickChildStderr(root, seen)
	if len(seen) != 2 {
		t.Fatalf("after prune seen = %d, want 2", len(seen))
	}
}
