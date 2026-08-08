package monitors

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/peerset"
)

func TestParseTCPTrafficLine_RealSample(t *testing.T) {
	// Real sample line lifted from a live mainnet peer's
	// data/tcp_traffic/hourly/<date>/<hour>. Three flows: one In, two Out.
	line := []byte(`["2026-05-25T10:37:08.672768629",[[["In","198.51.100.169",4001],0.8248638739917559],[["Out","203.0.113.75",4001],0.8252117484278043],[["Out","198.51.100.77",4001],0.8252116450033067]]]`)

	ts, in, out, ok := parseTCPTrafficLine(line)
	if !ok {
		t.Fatalf("parse returned ok=false on valid sample")
	}
	if ts.IsZero() {
		t.Errorf("expected non-zero timestamp")
	}
	if len(in) != 1 {
		t.Fatalf("expected 1 inbound flow, got %d", len(in))
	}
	if in[0].ip != "198.51.100.169" || in[0].value != 0.8248638739917559 {
		t.Errorf("inbound mismatch: %+v", in[0])
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 outbound flows, got %d", len(out))
	}
	// Outbound IPs are inserted via a map walk so order is non-deterministic;
	// assert presence by sum.
	outIPs := map[string]float64{}
	for _, p := range out {
		outIPs[p.ip] = p.value
	}
	if outIPs["203.0.113.75"] == 0 || outIPs["198.51.100.77"] == 0 {
		t.Errorf("missing expected outbound IP in %+v", out)
	}
}

func TestTrafficAdmissionRequiresTwoConsecutiveFreshTopSamples(t *testing.T) {
	state := newTCPTrafficMonitorState()
	state.registry = peerset.New(16, 24*time.Hour)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := tcpTrafficRecord{inbound: []peerSample{{ip: "192.0.2.1", value: 1}}}
	advanceTrafficAdmissions(record, t0, state)
	state.lastReceipt = t0
	if state.registry.Len() != 0 {
		t.Fatal("one observation admitted an endpoint")
	}
	advanceTrafficAdmissions(record, t0.Add(30*time.Second), state)
	if state.registry.Len() != 1 {
		t.Fatalf("two consecutive observations did not admit: len=%d", state.registry.Len())
	}

	state = newTCPTrafficMonitorState()
	state.registry = peerset.New(16, 24*time.Hour)
	advanceTrafficAdmissions(record, t0, state)
	state.lastReceipt = t0
	advanceTrafficAdmissions(record, t0.Add(91*time.Second), state)
	if state.registry.Len() != 0 {
		t.Fatal("a >90s gap did not reset the pending admission streak")
	}
}

func TestParseTCPTrafficLine_Malformed(t *testing.T) {
	cases := [][]byte{
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{}`),
		[]byte(`[1,2,3]`), // wrong outer arity
		[]byte(`["2026-01-01T00:00:00", "garbage"]`), // inner not array
	}
	for i, line := range cases {
		_, _, _, ok := parseTCPTrafficLine(line)
		if ok {
			t.Errorf("case %d: expected ok=false on malformed input %q", i, line)
		}
	}
}

func TestParseTCPTrafficLine_DedupesSameIPDifferentPorts(t *testing.T) {
	// One peer IP, two ports (4001 + 4002). Must collapse into a
	// single inbound row with summed values, otherwise dominant_inbound ends
	// up logging "ambiguous parent peer: top=X runner-up=X".
	line := []byte(`["2026-05-25T10:00:00",[[["In","1.1.1.1",4001],0.4],[["In","1.1.1.1",4002],0.6]]]`)
	_, in, _, ok := parseTCPTrafficLine(line)
	if !ok {
		t.Fatalf("parse failed")
	}
	if len(in) != 1 {
		t.Fatalf("expected 1 deduped inbound row, got %d (%+v)", len(in), in)
	}
	const want = 1.0
	if got := in[0].value; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("expected summed value %v, got %v", want, got)
	}
	if in[0].ip != "1.1.1.1" {
		t.Errorf("ip = %q", in[0].ip)
	}
}

func TestParseTCPTrafficLine_RejectsSnapshotWithBadFlow(t *testing.T) {
	line := []byte(`["2026-05-25T10:00:00",[[["In","1.1.1.1",4001],0.5],"junk",[["Out","2.2.2.2",4001],1.5]]]`)
	if _, _, _, ok := parseTCPTrafficLine(line); ok {
		t.Fatal("one malformed flow must reject the whole snapshot")
	}
}

func TestLastFullLine(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{"trailing newline", "a\nb\nc\n", "c", true},
		{"no trailing newline", "a\nb\nc", "b", true},
		{"single line no newline", "alone", "", false},
		{"empty", "", "", false},
		{"only newlines", "\n\n\n", "", false},
		{"torn last", "good\nbad", "good", true},
		{"trailing whitespace", "x\n   \n", "x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := lastFullLine([]byte(c.input))
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (input %q)", ok, c.wantOK, c.input)
			}
			if string(got) != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestParseTCPTrafficRecord_StrictCanonicalAggregation(t *testing.T) {
	line := []byte(`["2026-05-25T10:00:00",[[["In","::ffff:192.0.2.1",4001],0.4],[["In","192.0.2.1",4002],0.6],[["Out","2001:db8::2",6553],2.0]]]`)
	record, err := parseTCPTrafficRecord(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.inbound) != 1 || record.inbound[0].ip != "192.0.2.1" || record.inbound[0].value != 1 {
		t.Fatalf("unexpected canonical aggregate: %+v", record.inbound)
	}
	if got := record.byPort["in"]["4001"]; got != 0.4 {
		t.Fatalf("4001 aggregate=%v", got)
	}
	if got := record.byPort["in"]["4002"]; got != 0.6 {
		t.Fatalf("4002 aggregate=%v", got)
	}
	if got := record.byPort["out"]["other"]; got != 2 {
		t.Fatalf("other aggregate=%v", got)
	}
}

func TestParseTCPTrafficRecord_UsesConfiguredServicePortVocabulary(t *testing.T) {
	line := []byte(`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],1],[["In","192.0.2.2",5000],2]]]`)
	record, err := parseTCPTrafficRecordWithPorts(line, []uint16{5000})
	if err != nil {
		t.Fatal(err)
	}
	if got := record.byPort["in"]["5000"]; got != 2 {
		t.Fatalf("configured port aggregate=%v", got)
	}
	if got := record.byPort["in"]["other"]; got != 1 {
		t.Fatalf("unconfigured port aggregate=%v", got)
	}
}

func TestParseTCPTrafficRecord_DeterministicTieAndInvalidRows(t *testing.T) {
	line := []byte(`["2026-05-25T10:00:00",[[["In","192.0.2.2",4001],1],[["In","192.0.2.1",4001],1]]]`)
	record, err := parseTCPTrafficRecord(line)
	if err != nil {
		t.Fatal(err)
	}
	if record.inbound[0].ip != "192.0.2.1" || record.inbound[1].ip != "192.0.2.2" {
		t.Fatalf("tie order is not canonical: %+v", record.inbound)
	}
	bad := []string{
		`["2026-05-25T10:00:00",[[["In","not-ip",4001],1]]]`,
		`["2026-05-25T10:00:00",[[["In","fe80::1%eth0",4001],1]]]`,
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",0],1]]]`,
		`["2026-05-25T10:00:00",[[["Sideways","192.0.2.1",4001],1]]]`,
		`["bad-time",[]]`,
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],1e308],[["In","192.0.2.1",4002],1e308]]]`,
		`["2026-05-25T10:00:00",[[["In","192.0.2.1",4001],null]]]`,
	}
	for _, input := range bad {
		if _, err := parseTCPTrafficRecord([]byte(input)); err == nil {
			t.Errorf("accepted invalid record %s", input)
		}
	}
}

func TestUnixTimestampSecondsHandlesDatesBeyondUnixNanoRange(t *testing.T) {
	for _, year := range []int{2263, 9999} {
		at := time.Date(year, 12, 31, 23, 59, 59, 123456789, time.UTC)
		got := unixTimestampSeconds(at)
		if got < float64(at.Unix()) || got >= float64(at.Unix()+1) {
			t.Fatalf("year %d timestamp=%v outside [%d,%d)", year, got, at.Unix(), at.Unix()+1)
		}
	}
}

func TestTickTCPTraffic_RejectsSourceTimeRegression(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "20260808")
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dateDir, "3")
	newer := `["2026-08-08T03:00:30",[[["In","192.0.2.1",4001],2]]]` + "\n"
	older := `["2026-08-08T03:00:00",[[["In","192.0.2.2",4001],3]]]` + "\n"
	if err := os.WriteFile(path, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	state := newTCPTrafficMonitorState()
	tickTCPTraffic(root, state)
	want := state.lastSource
	if want.IsZero() {
		t.Fatal("newer snapshot did not commit")
	}
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	tickTCPTraffic(root, state)
	if !state.lastSource.Equal(want) || state.lastRecord != strings.TrimSpace(newer) {
		t.Fatalf("regressed snapshot committed: source=%s record=%q", state.lastSource, state.lastRecord)
	}
}

func TestTickTCPTrafficWithdrawsConfirmedAbsenceAndRepublishesSameRecord(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "20260808")
	path := filepath.Join(dateDir, "3")
	line := `["2026-08-08T03:00:30",[[["In","192.0.2.1",4001],2],[["Out","192.0.2.1",4001],3]]]` + "\n"
	write := func() {
		t.Helper()
		if err := os.MkdirAll(dateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() {
		metrics.HLP2PPeerTraffic.Reset()
		metrics.HLP2PTotalTraffic.Reset()
		metrics.HLP2PPeerCount.Reset()
		metrics.HLP2PTCPTrafficByServicePort.Reset()
		metrics.HLP2PTrafficEndpointsCurrent.Reset()
		metrics.HLP2PPeersTotal.Reset()
		metrics.HLP2PTCPTrafficSampleTimestampSeconds.Reset()
		metrics.HLP2PSampleAgeSeconds.Reset()
	}
	reset()
	t.Cleanup(reset)

	state := newTCPTrafficMonitorState([]uint16{4001})
	write()
	tickTCPTraffic(root, state)
	for name, collector := range map[string]prometheus.Collector{
		"peer":       metrics.HLP2PPeerTraffic,
		"total":      metrics.HLP2PTotalTraffic,
		"count":      metrics.HLP2PPeerCount,
		"port":       metrics.HLP2PTCPTrafficByServicePort,
		"endpoints":  metrics.HLP2PTrafficEndpointsCurrent,
		"alias":      metrics.HLP2PPeersTotal,
		"timestamp":  metrics.HLP2PTCPTrafficSampleTimestampSeconds,
		"sample_age": metrics.HLP2PSampleAgeSeconds,
	} {
		if rows := b03CollectorRows(t, collector); len(rows) == 0 {
			t.Fatalf("valid snapshot did not publish %s", name)
		}
	}

	if err := os.RemoveAll(dateDir); err != nil {
		t.Fatal(err)
	}
	tickTCPTraffic(root, state)
	for name, collector := range map[string]prometheus.Collector{
		"peer":       metrics.HLP2PPeerTraffic,
		"total":      metrics.HLP2PTotalTraffic,
		"count":      metrics.HLP2PPeerCount,
		"port":       metrics.HLP2PTCPTrafficByServicePort,
		"endpoints":  metrics.HLP2PTrafficEndpointsCurrent,
		"alias":      metrics.HLP2PPeersTotal,
		"timestamp":  metrics.HLP2PTCPTrafficSampleTimestampSeconds,
		"sample_age": metrics.HLP2PSampleAgeSeconds,
	} {
		if rows := b03CollectorRows(t, collector); len(rows) != 0 {
			t.Fatalf("confirmed absence retained %s rows: %d", name, len(rows))
		}
	}
	if sourceTime, receipt, in, out := LatestTCPTrafficObservation(); !sourceTime.IsZero() || !receipt.IsZero() || len(in) != 0 || len(out) != 0 {
		t.Fatalf("confirmed absence retained shared snapshot: source=%v receipt=%v in=%d out=%d", sourceTime, receipt, len(in), len(out))
	}

	write()
	tickTCPTraffic(root, state)
	if rows := b03CollectorRows(t, metrics.HLP2PPeerTraffic); len(rows) != 2 {
		t.Fatalf("same recovered record published %d peer rows, want 2", len(rows))
	}
}

func TestTCPTrafficProtectedGatherNeverSeesMixedGeneration(t *testing.T) {
	metrics.HLP2PPeerTraffic.Reset()
	metrics.HLP2PTotalTraffic.Reset()
	metrics.HLP2PPeerCount.Reset()
	state := newTCPTrafficMonitorState([]uint16{4001})
	record := func(ip string, value float64) tcpTrafficRecord {
		return tcpTrafficRecord{
			timestamp: time.Now(),
			inbound:   []peerSample{{ip: ip, dir: "in", value: value}},
			outbound:  []peerSample{{ip: ip, dir: "out", value: value}},
			byPort: map[string]map[string]float64{
				"in":  {"4001": value},
				"out": {"4001": value},
			},
		}
	}
	a := record("192.0.2.1", 1)
	b := record("192.0.2.2", 2)
	commitTCPTraffic(a, time.Now(), state)

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				commitTCPTraffic(b, time.Now(), state)
			} else {
				commitTCPTraffic(a, time.Now(), state)
			}
		}
	}()
	defer func() {
		close(stop)
		writer.Wait()
	}()

	gatherer := metrics.ProtectPrometheusSnapshots(prometheus.DefaultGatherer)
	for i := 0; i < 300; i++ {
		families, err := gatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]map[string]bool)
		for _, family := range families {
			if family.GetName() != "hl_p2p_peer_traffic" {
				continue
			}
			for _, row := range family.Metric {
				var ip, direction string
				for _, label := range row.Label {
					switch label.GetName() {
					case "ip":
						ip = label.GetValue()
					case "direction":
						direction = label.GetValue()
					}
				}
				if ip != "" && ip != "other" {
					if seen[ip] == nil {
						seen[ip] = make(map[string]bool)
					}
					seen[ip][direction] = true
				}
			}
		}
		aComplete := seen["192.0.2.1"]["in"] && seen["192.0.2.1"]["out"] && len(seen) == 1
		bComplete := seen["192.0.2.2"]["in"] && seen["192.0.2.2"]["out"] && len(seen) == 1
		if !aComplete && !bComplete {
			t.Fatalf("mixed traffic generation: %#v", seen)
		}
	}
}
