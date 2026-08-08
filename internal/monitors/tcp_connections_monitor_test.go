//go:build linux

package monitors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
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
	contents := `  sl local_address rem_address st rest
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

func TestReadTCPSource_AssociatesExactlyOneTrackedSide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	contents := `  sl local_address rem_address st rest
  0: 0100007F:0FA1 0200007F:0FA2 01 rest
  1: 0100007F:C350 0200007F:0FA2 01 rest
  2: 0100007F:C351 0200007F:0FA3 FF rest
  3: 0100007F:C352 0200007F:C353 01 rest
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	counts, stage, err := readTCPSource(path, []uint16{4001, 4002, 4003}, true)
	if err != nil {
		t.Fatalf("stage=%s err=%v", stage, err)
	}
	if got := counts[tcpSocketKey{port: 4001, side: "local", state: "ESTABLISHED"}]; got != 1 {
		t.Fatalf("local-first double match count=%d", got)
	}
	if got := counts[tcpSocketKey{port: 4002, side: "remote", state: "ESTABLISHED"}]; got != 1 {
		t.Fatalf("remote association count=%d", got)
	}
	if got := counts[tcpSocketKey{port: 4003, side: "remote", state: "UNKNOWN"}]; got != 1 {
		t.Fatalf("unknown state count=%d", got)
	}
	if got := counts[tcpSocketKey{port: 4002, side: "remote", state: "UNKNOWN"}]; got != 0 {
		t.Fatalf("unexpected double association=%d", got)
	}
}

func TestReadTCPSource_StrictFailureAndPortValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	if err := os.WriteFile(path, []byte("header\nmalformed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stage, err := readTCPSource(path, []uint16{4001}, true); err == nil || stage != "parse" {
		t.Fatalf("strict malformed row: stage=%q err=%v", stage, err)
	}
	if _, stage, err := readTCPSource(filepath.Join(dir, "missing"), []uint16{4001}, true); err == nil || stage != "open" {
		t.Fatalf("missing source: stage=%q err=%v", stage, err)
	}
	tooMany := make([]uint16, maxTCPServicePorts+1)
	for i := range tooMany {
		tooMany[i] = uint16(i + 1)
	}
	if _, err := validateTCPServicePorts(tooMany); err == nil {
		t.Fatal("accepted more than 16 configured service ports")
	}
	if _, err := validateTCPServicePorts([]uint16{4001, 4001}); err == nil {
		t.Fatal("accepted duplicate configured service port")
	}
}

func TestReadTCPSource_RejectsDataRowAsHeaderAndGarbageAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	dataRow := "0: 00000000:0FA1 00000000:0000 0A rest\n"
	if err := os.WriteFile(path, []byte(dataRow), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stage, err := readTCPSource(path, []uint16{4001}, true); err == nil || stage != "parse" {
		t.Fatalf("data row accepted as header: stage=%q err=%v", stage, err)
	}

	garbage := "sl local_address rem_address st rest\n0: GGGGGGGG:0FA1 00000000:0000 0A rest\n"
	if err := os.WriteFile(path, []byte(garbage), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stage, err := readTCPSource(path, []uint16{4001}, true); err == nil || stage != "parse" {
		t.Fatalf("garbage address accepted: stage=%q err=%v", stage, err)
	}
}

func TestValidTCPProcHeaderAcceptsKernelTCP4AndTCP6Spellings(t *testing.T) {
	for _, header := range []string{
		"sl local_address rem_address st rest",
		"sl local_address remote_address st rest",
	} {
		if !validTCPProcHeader(header) {
			t.Fatalf("rejected live kernel header %q", header)
		}
	}
	for _, header := range []string{
		"sl local_address remote st rest",
		"sl local_address rem_address state rest",
		"local_address rem_address st rest",
	} {
		if validTCPProcHeader(header) {
			t.Fatalf("accepted out-of-contract header %q", header)
		}
	}
}

func TestTCPConnectionsCombinedSnapshotAndAliasRollback(t *testing.T) {
	dir := t.TempDir()
	tcp4 := filepath.Join(dir, "tcp")
	tcp6 := filepath.Join(dir, "tcp6")
	header := "sl local_address rem_address st rest\n"
	header6 := "sl local_address remote_address st rest\n"
	row4 := "0: 0100007F:0FA1 0200007F:C350 01 rest\n"
	row6 := "0: 00000000000000000000000000000000:C350 00000000000000000000000000000001:0FA1 01 rest\n"
	if err := os.WriteFile(tcp4, []byte(header+row4), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tcp6, []byte(header6+row6), 0o600); err != nil {
		t.Fatal(err)
	}

	originalSources := procTCPSources
	procTCPSources = []struct {
		label string
		path  string
	}{{label: "tcp4", path: tcp4}, {label: "tcp6", path: tcp6}}
	defer func() { procTCPSources = originalSources }()
	metrics.HLP2PTCPSocketConnections.Reset()
	metrics.HLP2PTCPConnections.Reset()

	tickTCPConnections([]uint16{4001}, true)
	local := metrics.HLP2PTCPSocketConnections.WithLabelValues("4001", "local", "ESTABLISHED")
	remote := metrics.HLP2PTCPSocketConnections.WithLabelValues("4001", "remote", "ESTABLISHED")
	alias := metrics.HLP2PTCPConnections.WithLabelValues("4001", "ESTABLISHED")
	if got := hostMetricValue(t, local); got != 1 {
		t.Fatalf("local rows=%v", got)
	}
	if got := hostMetricValue(t, remote); got != 1 {
		t.Fatalf("remote TCP6 rows=%v", got)
	}
	if got := hostMetricValue(t, alias); got != 2 {
		t.Fatalf("compatibility alias=%v, want side sum 2", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketConnections.WithLabelValues("4001", "local", "UNKNOWN")); got != 0 {
		t.Fatalf("preseeded UNKNOWN=%v", got)
	}

	metrics.HLP2PTCPSocketLastSuccessTimestampSeconds.Set(42)
	if err := os.WriteFile(tcp6, []byte(header+"bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tickTCPConnections([]uint16{4001}, true)
	if got := hostMetricValue(t, local); got != 1 {
		t.Fatalf("failed combined read replaced local snapshot: %v", got)
	}
	if got := hostMetricValue(t, remote); got != 1 {
		t.Fatalf("failed combined read replaced remote snapshot: %v", got)
	}
	if got := hostMetricValue(t, alias); got != 2 {
		t.Fatalf("failed combined read replaced alias: %v", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketLastSuccessTimestampSeconds); got != 42 {
		t.Fatalf("failed combined read advanced last success: %v", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketSourceUp.WithLabelValues("tcp4")); got != 1 {
		t.Fatalf("successful tcp4 source_up=%v, want 1", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketSourceUp.WithLabelValues("tcp6")); got != 0 {
		t.Fatalf("failed tcp6 source_up=%v, want 0", got)
	}
}

func TestTCPConnectionsTornFinalRowRollsBackUntilCommitted(t *testing.T) {
	dir := t.TempDir()
	tcp4 := filepath.Join(dir, "tcp")
	tcp6 := filepath.Join(dir, "tcp6")
	header := "sl local_address rem_address st rest\n"
	row4 := "0: 0100007F:0FA1 0200007F:C350 01 rest\n"
	row6 := "0: 00000000000000000000000000000000:C350 00000000000000000000000000000001:0FA1 01 rest\n"
	if err := os.WriteFile(tcp4, []byte(header+row4), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tcp6, []byte(header+row6), 0o600); err != nil {
		t.Fatal(err)
	}

	originalSources := procTCPSources
	procTCPSources = []struct {
		label string
		path  string
	}{{label: "tcp4", path: tcp4}, {label: "tcp6", path: tcp6}}
	defer func() { procTCPSources = originalSources }()
	metrics.HLP2PTCPSocketConnections.Reset()
	metrics.HLP2PTCPConnections.Reset()
	tickTCPConnections([]uint16{4001}, true)
	remote := metrics.HLP2PTCPSocketConnections.WithLabelValues("4001", "remote", "ESTABLISHED")
	if got := hostMetricValue(t, remote); got != 1 {
		t.Fatalf("initial remote rows=%v, want 1", got)
	}

	metrics.HLP2PTCPSocketLastSuccessTimestampSeconds.Set(42)
	if err := os.WriteFile(tcp6, []byte(header+strings.TrimSuffix(row6, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	tickTCPConnections([]uint16{4001}, true)
	if got := hostMetricValue(t, remote); got != 1 {
		t.Fatalf("torn final row replaced prior snapshot: %v", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketLastSuccessTimestampSeconds); got != 42 {
		t.Fatalf("torn final row advanced last success: %v", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketSourceUp.WithLabelValues("tcp6")); got != 0 {
		t.Fatalf("torn TCP6 source_up=%v, want 0", got)
	}

	appendTestFile(t, tcp6, "\n")
	tickTCPConnections([]uint16{4001}, true)
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketSourceUp.WithLabelValues("tcp6")); got != 1 {
		t.Fatalf("committed TCP6 source_up=%v, want 1", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PTCPSocketLastSuccessTimestampSeconds); got == 42 {
		t.Fatal("committed row did not advance last success")
	}
}

func TestTCPConnectionsProtectedGatherBlocksPartialSuccessPublication(t *testing.T) {
	resetTCPConnectionTestMetrics()
	publishTCPConnectionsAttempt(tcpConnectionsTestAttempt(1, 1, ""), time.Unix(100, 0))

	families, returnedEarly := gatherDuringTCPPublication(
		t,
		tcpConnectionsTestAttempt(5, 7, ""),
		time.Unix(200, 0),
		tcpConnectionsPublishCounts,
	)
	if returnedEarly {
		t.Fatal("protected Gather returned while the success generation was paused mid-publication")
	}
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_connections", map[string]string{
		"service_port": "4001", "service_side": "local", "state": "ESTABLISHED",
	}, 5)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_connections", map[string]string{
		"service_port": "4001", "service_side": "remote", "state": "ESTABLISHED",
	}, 7)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_connections", map[string]string{
		"port": "4001", "state": "ESTABLISHED",
	}, 12)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_last_success_timestamp_seconds", nil, 200)
}

func TestTCPConnectionsProtectedGatherBlocksPartialOneSourceFailure(t *testing.T) {
	resetTCPConnectionTestMetrics()
	publishTCPConnectionsAttempt(tcpConnectionsTestAttempt(2, 3, ""), time.Unix(300, 0))

	families, returnedEarly := gatherDuringTCPPublication(
		t,
		tcpConnectionsTestAttempt(50, 70, "parse"),
		time.Unix(400, 0),
		tcpConnectionsPublishSources,
	)
	if returnedEarly {
		t.Fatal("protected Gather returned while the one-source failure generation was paused")
	}
	// Failed attempts publish source health/errors atomically but retain every
	// value belonging to the last complete combined data generation.
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_source_up", map[string]string{"source": "tcp4"}, 1)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_source_up", map[string]string{"source": "tcp6"}, 0)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_connections", map[string]string{
		"service_port": "4001", "service_side": "local", "state": "ESTABLISHED",
	}, 2)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_connections", map[string]string{
		"service_port": "4001", "service_side": "remote", "state": "ESTABLISHED",
	}, 3)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_connections", map[string]string{
		"port": "4001", "state": "ESTABLISHED",
	}, 5)
	assertGatheredTCPMetric(t, families, "hl_p2p_tcp_socket_last_success_timestamp_seconds", nil, 300)
}

func resetTCPConnectionTestMetrics() {
	metrics.HLP2PTCPSocketConnections.Reset()
	metrics.HLP2PTCPConnections.Reset()
	metrics.HLP2PTCPSocketSourceUp.Reset()
	metrics.HLP2PTCPSocketErrorsTotal.Reset()
	metrics.HLP2PTCPSocketLastSuccessTimestampSeconds.Set(0)
}

func tcpConnectionsTestAttempt(local, remote int, tcp6FailureStage string) tcpConnectionsAttempt {
	ports := []uint16{4001}
	combined := preseedTCPSocketCounts(ports)
	combined[tcpSocketKey{port: 4001, side: "local", state: "ESTABLISHED"}] = local
	combined[tcpSocketKey{port: 4001, side: "remote", state: "ESTABLISHED"}] = remote
	return tcpConnectionsAttempt{
		ports:      ports,
		enableTCP6: true,
		outcomes: []tcpSourceReadOutcome{
			{label: "tcp4", ok: true},
			{label: "tcp6", stage: tcp6FailureStage, ok: tcp6FailureStage == ""},
		},
		combined: combined,
	}
}

type tcpGatherResult struct {
	families []*dto.MetricFamily
	err      error
}

func gatherDuringTCPPublication(
	t *testing.T,
	attempt tcpConnectionsAttempt,
	now time.Time,
	pauseAt tcpConnectionsPublishPhase,
) ([]*dto.MetricFamily, bool) {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		metrics.HLP2PTCPSocketConnections,
		metrics.HLP2PTCPConnections,
		metrics.HLP2PTCPSocketSourceUp,
		metrics.HLP2PTCPSocketLastSuccessTimestampSeconds,
	)
	paused := make(chan struct{})
	release := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		publishTCPConnectionsAttemptWithCheckpoint(attempt, now, func(phase tcpConnectionsPublishPhase) {
			if phase != pauseAt {
				return
			}
			close(paused)
			<-release
		})
	}()

	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		close(release)
		<-writerDone
		t.Fatal("TCP publication did not reach the requested checkpoint")
	}

	gatherStarted := make(chan struct{})
	gathered := make(chan tcpGatherResult, 1)
	go func() {
		close(gatherStarted)
		families, err := metrics.ProtectPrometheusSnapshots(registry).Gather()
		gathered <- tcpGatherResult{families: families, err: err}
	}()
	<-gatherStarted

	var result tcpGatherResult
	returnedEarly := false
	select {
	case result = <-gathered:
		returnedEarly = true
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-writerDone
	if !returnedEarly {
		result = <-gathered
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.families, returnedEarly
}

func assertGatheredTCPMetric(
	t *testing.T,
	families []*dto.MetricFamily,
	name string,
	labels map[string]string,
	want float64,
) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			if !tcpMetricLabelsEqual(metric, labels) {
				continue
			}
			if got := metric.GetGauge().GetValue(); got != want {
				t.Fatalf("%s%v=%v, want %v", name, labels, got, want)
			}
			return
		}
	}
	t.Fatalf("metric %s%v not gathered", name, labels)
}

func tcpMetricLabelsEqual(metric *dto.Metric, want map[string]string) bool {
	if len(metric.Label) != len(want) {
		return false
	}
	for _, pair := range metric.Label {
		if want[pair.GetName()] != pair.GetValue() {
			return false
		}
	}
	return true
}

func TestActiveProcTCPSources_DisabledTCP6IsNotAttempted(t *testing.T) {
	withoutTCP6 := activeProcTCPSources(false)
	if len(withoutTCP6) != 1 || withoutTCP6[0].label != "tcp4" {
		t.Fatalf("disabled TCP6 sources=%v", withoutTCP6)
	}
	withTCP6 := activeProcTCPSources(true)
	if len(withTCP6) != 2 || withTCP6[1].label != "tcp6" {
		t.Fatalf("enabled TCP6 sources=%v", withTCP6)
	}
}
