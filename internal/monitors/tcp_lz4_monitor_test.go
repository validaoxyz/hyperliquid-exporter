package monitors

import (
	"testing"
)

func TestParseLz4PeerLine_RealSample(t *testing.T) {
	// Trimmed real sample from a live mainnet peer's tcp_lz4_stats file.
	// Two peers, one Out one In.
	line := []byte(`["2026-05-25T10:44:59.848700563",[[["Out","203.0.113.79",4001],342919728,4454,0.6510],[["In","198.51.100.169",4001],601888685,4474,0.6208]]]`)
	peers, ok := parseLz4PeerLine(line)
	if !ok {
		t.Fatalf("ok=false on valid sample")
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	// Order-preserving from input.
	if peers[0].direction != "out" || peers[0].ip != "203.0.113.79" || peers[0].bytes != 342919728 || peers[0].ratio != 0.6510 {
		t.Errorf("peer 0 mismatch: %+v", peers[0])
	}
	if peers[1].direction != "in" || peers[1].ip != "198.51.100.169" || peers[1].packets != 4474 {
		t.Errorf("peer 1 mismatch: %+v", peers[1])
	}
}

func TestParseLz4PeerLine_RejectsGlobalLine(t *testing.T) {
	// Global aggregate row — must NOT parse as a peer line.
	line := []byte(`["2026-05-25T10:44:59.848744913",[5416937682,40265,0.6208]]`)
	_, ok := parseLz4PeerLine(line)
	if ok {
		t.Errorf("global line should not parse as peer line")
	}
}

func TestParseLz4GlobalLine_RealSample(t *testing.T) {
	line := []byte(`["2026-05-25T10:44:59.848744913",[5416937682,40265,0.6208]]`)
	b, p, r, ok := parseLz4GlobalLine(line)
	if !ok {
		t.Fatalf("ok=false")
	}
	if b != 5416937682 || p != 40265 || r != 0.6208 {
		t.Errorf("got bytes=%d packets=%d ratio=%v", b, p, r)
	}
}

func TestParseLz4GlobalLine_RejectsPeerLine(t *testing.T) {
	// Peer line — outer inner is a 2-element array, not 3 numbers.
	line := []byte(`["2026-05-25T10:44:59.848700563",[[["Out","1.2.3.4",4001],1,1,0.5]]]`)
	_, _, _, ok := parseLz4GlobalLine(line)
	if ok {
		t.Errorf("peer line should not parse as global line")
	}
}

func TestParseLz4Lines_Malformed(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{}`),
		[]byte(`[1]`),
		[]byte(`["ts", []]`),
		[]byte(`["ts", "x"]`),
	} {
		if _, ok := parseLz4PeerLine(line); ok {
			t.Errorf("peer parser should reject %q", line)
		}
		if _, _, _, ok := parseLz4GlobalLine(line); ok {
			t.Errorf("global parser should reject %q", line)
		}
	}
}
