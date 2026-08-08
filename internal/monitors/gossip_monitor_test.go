package monitors

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
)

func TestParseChildSnapshotLine_StrictCanonicalSnapshot(t *testing.T) {
	line := []byte(`["2026-08-08T03:00:00",["child_peers status",[["::ffff:192.0.2.2",{"verified":true,"connection_count":2}],["192.0.2.1",{"verified":false,"connection_count":1}]]]]`)
	snapshot, isChild, err := parseChildSnapshotLine(line)
	if err != nil || !isChild {
		t.Fatalf("isChild=%v err=%v", isChild, err)
	}
	if len(snapshot.peers) != 2 || snapshot.peers[0].ip != "192.0.2.1" || snapshot.peers[1].ip != "192.0.2.2" {
		t.Fatalf("canonical sorted peers=%+v", snapshot.peers)
	}
	if snapshot.peers[1].connections != 2 || !snapshot.peers[1].verified {
		t.Fatalf("status=%+v", snapshot.peers[1])
	}
}

func TestParseChildSnapshotLine_RejectsAnyBadOrDuplicateRow(t *testing.T) {
	cases := []string{
		`["2026-08-08T03:00:00",["child_peers status",[["not-ip",{"verified":true,"connection_count":1}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":-1}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":1}],["::ffff:192.0.2.1",{"verified":false,"connection_count":2}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":null,"connection_count":1}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":null}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":9223372036854775807}],["192.0.2.2",{"verified":true,"connection_count":1}]]]]`,
		`["2026-08-08T03:00:00",["child_peers status",[["fe80::1%eth0",{"verified":true,"connection_count":1}]]]]`,
	}
	for _, input := range cases {
		if _, isChild, err := parseChildSnapshotLine([]byte(input)); !isChild || err == nil {
			t.Errorf("accepted invalid child snapshot: child=%v err=%v input=%s", isChild, err, input)
		}
	}
}

func TestReadLatestChildSnapshot_MalformedNewestRollsBack(t *testing.T) {
	good := `["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":1}]]]]`
	for name, bad := range map[string]string{
		"bad row":          `["2026-08-08T03:00:30",["child_peers status",[["bad",{"verified":true,"connection_count":1}]]]]`,
		"outer arity":      `["2026-08-08T03:00:30",["child_peers status",[]],"extra"]`,
		"payload scalar":   `["2026-08-08T03:00:30","child_peers status"]`,
		"syntax after tag": `["2026-08-08T03:00:30",["child_peers status",[`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hour")
			if err := os.WriteFile(path, []byte(good+"\n"+bad+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readLatestChildSnapshot(path); err == nil {
				t.Fatal("malformed newest child snapshot fell back to an older snapshot")
			}
		})
	}
}

func TestReadLatestChildSnapshot_IgnoresTagTextOutsideEventPosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hour")
	good := `["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":1}]]]]`
	unrelated := `["2026-08-08T03:00:30",["other","child_peers status"]]`
	if err := os.WriteFile(path, []byte(good+"\n"+unrelated+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := readLatestChildSnapshot(path)
	if err != nil || !found || len(snapshot.peers) != 1 || snapshot.peers[0].ip != "192.0.2.1" {
		t.Fatalf("unrelated payload blocked older explicit child snapshot: found=%v snapshot=%+v err=%v", found, snapshot, err)
	}
}

func TestGossipTickRescansSamePathAndSizeReplacement(t *testing.T) {
	nodeHome := t.TempDir()
	hourly := filepath.Join(nodeHome, "data", "node_logs", "gossip_rpc", "hourly", "20260808")
	if err := os.MkdirAll(hourly, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(hourly, "3")
	a := `["2026-08-08T03:00:00",["child_peers status",[["192.0.2.1",{"verified":true,"connection_count":1}]]]]` + "\n"
	b := `["2026-08-08T03:00:30",["child_peers status",[["192.0.2.2",{"verified":true,"connection_count":1}]]]]` + "\n"
	if len(a) != len(b) {
		t.Fatalf("fixture sizes differ: %d/%d", len(a), len(b))
	}
	if err := os.WriteFile(path, []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewGossipMonitor(&config.Config{NodeHome: nodeHome})
	t0 := time.Date(2026, 8, 8, 3, 0, 1, 0, time.UTC)
	m.tick(t0)
	if _, ok := m.current["192.0.2.1"]; !ok {
		t.Fatalf("first snapshot not committed: %+v", m.current)
	}
	if err := os.WriteFile(path, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	m.tick(t0.Add(30 * time.Second))
	if _, old := m.current["192.0.2.1"]; old {
		t.Fatalf("same-size replacement retained old child: %+v", m.current)
	}
	if _, ok := m.current["192.0.2.2"]; !ok {
		t.Fatalf("same-size replacement was not rescanned: %+v", m.current)
	}
}

func TestChildSnapshotReplacementEmptyStaleAndIdentityCap(t *testing.T) {
	cfg := &config.Config{EnablePerPeerMetrics: true}
	m := NewGossipMonitor(cfg)
	t0 := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	a := childPeer{ip: "192.0.2.1", verified: true, connections: 1}
	b := childPeer{ip: "192.0.2.2", connections: 2}
	m.commitSnapshot(childSnapshot{sourceTime: t0, peers: []childPeer{a, b}}, t0)
	m.commitSnapshot(childSnapshot{sourceTime: t0.Add(30 * time.Second), peers: []childPeer{b}}, t0.Add(30*time.Second))
	if len(m.current) != 1 || m.current[b.ip].connections != 2 {
		t.Fatalf("replacement did not remove A: %+v", m.current)
	}
	m.commitSnapshot(childSnapshot{sourceTime: t0.Add(time.Minute), peers: nil}, t0.Add(time.Minute))
	if len(m.current) != 0 {
		t.Fatalf("fresh empty did not clear: %+v", m.current)
	}

	peers := make([]childPeer, 20)
	for i := range peers {
		peers[i] = childPeer{ip: fmt.Sprintf("192.0.2.%d", i+1), connections: 1}
	}
	m.commitSnapshot(childSnapshot{sourceTime: t0.Add(90 * time.Second), peers: peers}, t0.Add(90*time.Second))
	if len(m.current) != 20 || len(m.published) != childIdentityCap {
		t.Fatalf("aggregate/cap mismatch: current=%d published=%d", len(m.current), len(m.published))
	}
	m.publishFreshness(t0.Add(181 * time.Second))
	if len(m.current) != 0 || len(m.published) != 0 || !m.clearedStale {
		t.Fatalf("91-second stale state not cleared: current=%d published=%d", len(m.current), len(m.published))
	}
}

func TestChildFreshnessUsesReceiptNotFutureSourceTime(t *testing.T) {
	m := NewGossipMonitor(&config.Config{})
	receipt := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	m.commitSnapshot(childSnapshot{sourceTime: receipt.Add(24 * time.Hour)}, receipt)
	m.publishFreshness(receipt.Add(91 * time.Second))
	if !m.clearedStale {
		t.Fatal("future source timestamp prevented receipt-based staleness")
	}
}
