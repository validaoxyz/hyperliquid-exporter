package monitors

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
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

func TestSelectLatestLZ4Pair_NonAdjacentWithinTolerance(t *testing.T) {
	data := []byte(
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-05-25T10:00:01",[19,2,0.55]]` + "\n" +
			`["2026-05-25T10:00:02",[20,2,0.6]]` + "\n")
	pair, err := selectLatestLZ4Pair(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(pair.peer.peers) != 1 || pair.global.bytes != 20 {
		t.Fatalf("unexpected pair: %+v", pair)
	}
}

func TestSelectLatestLZ4Pair_SkipsDanglingNewHalfWindow(t *testing.T) {
	data := []byte(
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-05-25T10:00:01",[20,2,0.6]]` + "\n" +
			`["2026-05-25T10:05:00",[[["In","192.0.2.2",4001],30,3,0.7]]]` + "\n")
	pair, err := selectLatestLZ4Pair(data)
	if err != nil {
		t.Fatal(err)
	}
	if !pair.peer.timestamp.Equal(time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)) || pair.global.bytes != 20 {
		t.Fatalf("did not retain newest complete pair: %+v", pair)
	}
}

func TestSelectLatestLZ4Pair_SearchesFullBoundedSuffixBySourceTime(t *testing.T) {
	// Append order is deliberately not source-time order. A reverse scan first
	// encounters the dangling 10:10 peer, then a complete 10:00 pair. The
	// matching 10:10 global is older in append position and must still win.
	data := []byte(
		`["2026-05-25T10:10:01",[40,4,0.8]]` + "\n" +
			`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-05-25T10:00:01",[20,2,0.6]]` + "\n" +
			`["2026-05-25T10:10:00",[[["In","192.0.2.2",4001],30,3,0.7]]]` + "\n")
	pair, err := selectLatestLZ4Pair(data)
	if err != nil {
		t.Fatal(err)
	}
	if !pairTimestamp(pair).Equal(time.Date(2026, 5, 25, 10, 10, 1, 0, time.UTC)) ||
		len(pair.peer.peers) != 1 || pair.peer.peers[0].ip != "192.0.2.2" || pair.global.bytes != 40 {
		t.Fatalf("did not select newest source-time pair from full suffix: %+v", pair)
	}
}

func TestTickLZ4_SameTimestampContentReplacementReconciles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "20260808")
	prior := []byte(
		`["2026-08-08T02:55:00",[[["In","192.0.2.9",4001],12,2,0.5]]]` + "\n" +
			`["2026-08-08T02:55:01",[22,2,0.6]]` + "\n")
	first := []byte(
		`["2026-08-08T03:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-08-08T03:00:01",[20,2,0.6]]` + "\n")
	replacement := []byte(
		`["2026-08-08T03:00:00",[[["In","192.0.2.2",4001],5,1,0.4]]]` + "\n" +
			`["2026-08-08T03:00:01",[15,1,0.5]]` + "\n")
	if err := os.WriteFile(path, prior, 0o600); err != nil {
		t.Fatal(err)
	}

	metrics.HLP2PLZ4WindowBytes.Reset()
	metrics.HLP2PLZ4WindowPackets.Reset()
	metrics.HLP2PLZ4WindowWeightedRatio.Reset()
	state := newLz4MonitorState([]uint16{4001})
	tickLz4(root, state)
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	tickLz4(root, state)
	firstKey := state.lastPairKey
	if firstKey == "" || !state.prevPublished["in"]["192.0.2.1"] {
		t.Fatalf("first pair did not publish: key=%q labels=%v", firstKey, state.prevPublished)
	}

	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	tickLz4(root, state)
	if state.lastPairKey == firstKey {
		t.Fatal("same-timestamp content replacement retained the old pair identity")
	}
	if state.prevPublished["in"]["192.0.2.1"] || !state.prevPublished["in"]["192.0.2.2"] {
		t.Fatalf("replacement did not reconcile endpoint labels: %v", state.prevPublished["in"])
	}
	if got := hostMetricValue(t, metrics.HLP2PLZ4WindowBytes.WithLabelValues("192.0.2.2", "in")); got != 5 {
		t.Fatalf("replacement endpoint bytes=%v, want 5", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PLZ4GlobalWindowBytes); got != 15 {
		t.Fatalf("replacement global bytes=%v, want 15", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PLZ4WindowDurationSeconds); got != 300 {
		t.Fatalf("same-window correction erased prior window duration: got %v want 300", got)
	}
}

func TestSelectLatestLZ4Pair_RejectsCrossWindowAndRetainsPriorPairOnTornTail(t *testing.T) {
	crossWindow := []byte(
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-05-25T10:05:00",[20,2,0.6]]` + "\n")
	if _, err := selectLatestLZ4Pair(crossWindow); err == nil {
		t.Fatal("paired records 300 seconds apart")
	}
	torn := []byte(
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-05-25T10:00:01",[20,2,0.6]]` + "\n" +
			`["2026-05-25T10:05:00",[[["In","192.0.2.2",4001],30,3,0.7]]]` + "\n" +
			`["2026-05-25T10:05:01",[40`)
	pair, err := selectLatestLZ4Pair(torn)
	if err != nil {
		t.Fatal(err)
	}
	if !pairTimestamp(pair).Equal(time.Date(2026, 5, 25, 10, 0, 1, 0, time.UTC)) || pair.global.bytes != 20 {
		t.Fatalf("torn tail did not retain prior complete pair: %+v", pair)
	}
}

func TestSelectLatestLZ4Pair_AcceptsCompleteFinalRecordWithoutNewline(t *testing.T) {
	data := []byte(
		`["2026-08-09T00:00:04.247163972",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-08-09T00:00:04.247308733",[20,2,0.6]]`)
	pair, err := selectLatestLZ4Pair(data)
	if err != nil {
		t.Fatal(err)
	}
	if pair.global.bytes != 20 || len(pair.peer.peers) != 1 || pair.peer.peers[0].ip != "192.0.2.1" {
		t.Fatalf("unexpected unterminated-source pair: %+v", pair)
	}
}

func TestSelectLatestLZ4Pair_RejectsCompleteInvalidFinalRecord(t *testing.T) {
	data := []byte(
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-05-25T10:00:01",[20,2,0.6]]` + "\n" +
			`["2026-05-25T10:05:00",{"unexpected":true}]`)
	if _, err := selectLatestLZ4Pair(data); err == nil {
		t.Fatal("accepted a complete but invalid final record")
	}
}

func TestTickLZ4_DailyRolloverAcceptsCompleteFinalRecordWithoutNewline(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "20260808")
	newPath := filepath.Join(root, "20260809")
	oldWindow := []byte(
		`["2026-08-08T23:55:04.245441218",[[["In","192.0.2.1",4001],10,2,0.5]]]` + "\n" +
			`["2026-08-08T23:55:04.245477629",[20,2,0.6]]`)
	newWindow := []byte(
		`["2026-08-09T00:00:04.247163972",[[["In","192.0.2.2",4001],30,3,0.7]]]` + "\n" +
			`["2026-08-09T00:00:04.247308733",[40,3,0.8]]`)
	if err := os.WriteFile(oldPath, oldWindow, 0o600); err != nil {
		t.Fatal(err)
	}
	state := newLz4MonitorState([]uint16{4001})
	monitorErrors := metrics.HLExporterMonitorErrorsTotal.WithLabelValues("tcp_lz4")
	sourceErrors := metrics.HLExporterSourceErrorsTotal.WithLabelValues(string(metrics.SourceTCPLZ4), string(metrics.SourceFailureSchema))
	monitorBefore := hostMetricValue(t, monitorErrors)
	sourceBefore := hostMetricValue(t, sourceErrors)
	tickLz4(root, state)
	oldSource := state.previousSource
	if oldSource.IsZero() {
		t.Fatal("old-day pair did not publish")
	}
	if err := os.WriteFile(newPath, newWindow, 0o600); err != nil {
		t.Fatal(err)
	}
	tickLz4(root, state)
	if !state.previousSource.After(oldSource) {
		t.Fatalf("daily rollover did not advance source time: old=%s new=%s", oldSource, state.previousSource)
	}
	if delta := hostMetricValue(t, monitorErrors) - monitorBefore; delta != 0 {
		t.Fatalf("daily rollover monitor error delta=%v", delta)
	}
	if delta := hostMetricValue(t, sourceErrors) - sourceBefore; delta != 0 {
		t.Fatalf("daily rollover source error delta=%v", delta)
	}
}

func TestLZ4MultiportAggregationUsesByteWeightedReportedRatio(t *testing.T) {
	agg := &lz4Aggregate{}
	if err := addLz4Sample(agg, lz4PeerSample{bytes: 100, packets: 2, ratio: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := addLz4Sample(agg, lz4PeerSample{bytes: 300, packets: 4, ratio: 0.9}); err != nil {
		t.Fatal(err)
	}
	if agg.bytes != 400 || agg.packets != 6 {
		t.Fatalf("aggregate=%+v", agg)
	}
	if got, want := lz4Ratio(agg), 0.8; math.Abs(got-want) > 1e-12 {
		t.Fatalf("weighted ratio=%v want=%v", got, want)
	}
}

func TestPrepareLZ4Peers_ZeroIdentityAndNumericOverflow(t *testing.T) {
	prepared, err := prepareLz4Peers([]lz4PeerSample{
		{direction: "in", ip: "192.0.2.1", port: 4001},
		{direction: "in", ip: "192.0.2.2", port: 4001, bytes: 1, packets: 1, ratio: 0.5},
	}, tcpListenPorts)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.directions["in"].top; len(got) != 1 || got[0].ip != "192.0.2.2" {
		t.Fatalf("zero-only row created identity detail: %+v", got)
	}

	maxInt64 := int64(^uint64(0) >> 1)
	prepared, err = prepareLz4Peers([]lz4PeerSample{
		{direction: "out", ip: "192.0.2.3", port: 4001, bytes: maxInt64, packets: maxInt64, ratio: 1},
		{direction: "out", ip: "192.0.2.3", port: 4002, bytes: maxInt64, packets: maxInt64, ratio: 1},
	}, tcpListenPorts)
	if err != nil {
		t.Fatalf("representable Prometheus aggregate was rejected: %v", err)
	}
	agg := prepared.directions["out"].top[0].agg
	if agg.bytes <= 0 || math.IsInf(agg.bytes, 0) || lz4Ratio(agg) != 1 {
		t.Fatalf("large aggregate wrapped or corrupted: %+v", agg)
	}

	if _, err := prepareLz4Peers([]lz4PeerSample{
		{direction: "in", ip: "192.0.2.4", port: 4001, bytes: 2, packets: 1, ratio: math.MaxFloat64},
	}, tcpListenPorts); err == nil {
		t.Fatal("accepted non-finite weighted-ratio aggregate")
	}
}

func TestParseLZ4PeerLine_CompleteEmptyIsValid(t *testing.T) {
	peers, ok := parseLz4PeerLine([]byte(`["2026-05-25T10:00:00",[]]`))
	if !ok || len(peers) != 0 {
		t.Fatalf("fresh empty peer record: ok=%v peers=%v", ok, peers)
	}
	if tcpLz4IdentityTTL != 2*5*time.Minute {
		t.Fatalf("identity expiry must remain two 5-minute windows, got %s", tcpLz4IdentityTTL)
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
		[]byte(`["2026-05-25T10:44:59",[null,1,0.5]]`),
		[]byte(`["2026-05-25T10:44:59",[[["In","192.0.2.1",4001],null,1,0.5]]]`),
		[]byte(`["2026-05-25T10:44:59",[[["In","fe80::1%eth0",4001],1,1,0.5]]]`),
	} {
		if _, ok := parseLz4PeerLine(line); ok {
			t.Errorf("peer parser should reject %q", line)
		}
		if _, _, _, ok := parseLz4GlobalLine(line); ok {
			t.Errorf("global parser should reject %q", line)
		}
	}
}
