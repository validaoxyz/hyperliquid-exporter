package monitors

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"go.opentelemetry.io/otel"
)

func TestConsensusEOFDoesNotAdvanceObservation(t *testing.T) {
	const childEnv = "HL_EXPORTER_TEST_CONSENSUS_EOF_CHILD"
	if os.Getenv(childEnv) == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConsensusEOFDoesNotAdvanceObservation$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated consensus lifecycle test failed: %v\n%s", err, output)
		}
		return
	}

	nodeHome := t.TempDir()
	logDir := filepath.Join(nodeHome, "data", "node_logs", "consensus", "hourly", "20260808")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "0")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	lineCounter, err := otel.Meter("consensus-eof-lifecycle-test").Int64Counter("test_consensus_lines")
	if err != nil {
		t.Fatal(err)
	}
	oldLineCounter := metrics.HLConsensusMonitorLinesCounter
	metrics.HLConsensusMonitorLinesCounter = lineCounter
	defer func() { metrics.HLConsensusMonitorLinesCounter = oldLineCounter }()
	metrics.RegisterSource(metrics.SourceConsensus, true)
	m := NewConsensusMonitor(&config.Config{NodeHome: nodeHome})
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.monitorConsensusLogs(ctx, make(chan error, 1))
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("consensus tailer did not stop")
		}
	}()

	openDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(openDeadline) {
		metrics.PublishMonitorHealthSnapshot()
		if value, ok := b03CollectorValue(t, metrics.HLExporterSourcePresent, map[string]string{"source": "consensus"}); ok && value == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if value, ok := b03CollectorValue(t, metrics.HLExporterSourcePresent, map[string]string{"source": "consensus"}); !ok || value != 1 {
		t.Fatal("consensus tailer did not open the empty startup file")
	}

	// Give the open tailer several EOF pauses. Merely polling an empty
	// committed file must not manufacture a valid observation.
	time.Sleep(175 * time.Millisecond)
	metrics.PublishMonitorHealthSnapshot()
	labels := map[string]string{"monitor": "consensus"}
	if validatorCollectorHasLabels(metrics.HLExporterMonitorLastValidSeconds, labels) {
		t.Fatal("EOF-only consensus stream advanced last-valid observation")
	}
	if validatorCollectorHasLabels(metrics.HLExporterMonitorLastPublicationSeconds, labels) {
		t.Fatal("EOF-only consensus stream advanced last publication")
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("[\"2026-08-08T00:00:00.000000000\",[\"round advance\",{\"prev_round\":1,\"round\":2,\"reason\":\"Qc\"}]]\n")
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		metrics.PublishMonitorHealthSnapshot()
		if validatorCollectorHasLabels(metrics.HLExporterMonitorLastValidSeconds, labels) &&
			validatorCollectorHasLabels(metrics.HLExporterMonitorLastPublicationSeconds, labels) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("valid committed consensus record did not advance progress")
}

func TestConsensusBlockTypedNullAndBoundedDeduplication(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want bool
	}{
		"missing": {"", false},
		"null":    {"null", false},
		"object":  {`{"timeouts":[]}`, true},
		"array":   {`[]`, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isJSONObject(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("isJSONObject(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}

	dedupe := newBoundedBlockDedupe(2)
	if dedupe.seenOrAdd("a") || !dedupe.seenOrAdd("a") || dedupe.seenOrAdd("b") || dedupe.seenOrAdd("c") {
		t.Fatal("unexpected bounded dedupe transition")
	}
	if dedupe.seenOrAdd("a") {
		t.Fatal("oldest key was not evicted at capacity")
	}
}

func TestProcessConsensusLineRejectsNullDirectionBeforeMutation(t *testing.T) {
	for _, line := range []string{
		`["2026-08-08T00:00:00.000000000",[null,{"Block":{"round":42,"proposer":"p","hash":"null-direction","tc":null}}]]`,
		`["2026-08-08T00:00:00.000000000",[null,null]]`,
	} {
		m := NewConsensusMonitor(&config.Config{})
		otherBefore := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("other"))
		if err := m.processConsensusLine(line); err == nil {
			t.Fatalf("null direction accepted: %s", line)
		}
		if got := m.GetVerificationStats().BlocksProcessed; got != 0 {
			t.Fatalf("null direction processed %d blocks", got)
		}
		if got := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("other")); got != otherBefore {
			t.Fatalf("null direction changed direction counter from %v to %v", otherBefore, got)
		}
	}
}

func TestRoundAdvanceRejectsNullRequiredFields(t *testing.T) {
	for name, raw := range map[string]string{
		"prev_round": `{"prev_round":null,"round":12,"reason":"Qc"}`,
		"round":      `{"prev_round":11,"round":null,"reason":"Qc"}`,
		"reason":     `{"prev_round":11,"round":12,"reason":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := NewConsensusMonitor(&config.Config{})
			before := validatorMetricValue(t, metrics.HLConsensusRoundAdvanceEvents.WithLabelValues("other"))
			if err := m.processRoundAdvance(json.RawMessage(raw), time.Now()); err == nil {
				t.Fatalf("accepted null %s", name)
			}
			if m.latestConsensusRound != 0 {
				t.Fatalf("null %s advanced local round to %d", name, m.latestConsensusRound)
			}
			if got := validatorMetricValue(t, metrics.HLConsensusRoundAdvanceEvents.WithLabelValues("other")); got != before {
				t.Fatalf("null %s incremented bounded other counter", name)
			}
		})
	}

	m := NewConsensusMonitor(&config.Config{})
	if err := m.processRoundAdvance(json.RawMessage(`{"prev_round":0,"round":1,"reason":"future_reason"}`), time.Now()); err != nil {
		t.Fatalf("valid zero prev_round/future reason rejected: %v", err)
	}
}

func TestLocalConsensusStatusRejectsNullOperands(t *testing.T) {
	for _, field := range []string{"round", "last_vote_round", "last_commit_round", "qc_round"} {
		t.Run(field, func(t *testing.T) {
			values := map[string]string{
				"round": "12", "last_vote_round": "12", "last_commit_round": "10", "qc_round": "11",
			}
			values[field] = "null"
			raw := fmt.Sprintf(`{"round":%s,"last_vote_round":%s,"last_commit_round":%s,"qc_round":%s}`,
				values["round"], values["last_vote_round"], values["last_commit_round"], values["qc_round"])
			m := NewConsensusMonitor(&config.Config{})
			if err := m.processLocalConsensusStatus(json.RawMessage(raw), time.Now()); err == nil {
				t.Fatalf("accepted null %s", field)
			}
			if m.latestConsensusRound != 0 {
				t.Fatalf("null %s advanced local round to %d", field, m.latestConsensusRound)
			}
		})
	}
}

func TestConsensusQCRejectsNullOrIncompleteCertificateAtomically(t *testing.T) {
	for name, qc := range map[string]string{
		"round":        `{"round":null,"block_hash":"q","signers":[]}`,
		"block_hash":   `{"round":99,"block_hash":null,"signers":[]}`,
		"signers":      `{"round":99,"block_hash":"q","signers":null}`,
		"signer entry": `{"round":99,"block_hash":"q","signers":[null]}`,
		"empty object": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := NewConsensusMonitor(&config.Config{})
			before := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("in"))
			block := json.RawMessage(fmt.Sprintf(`{"round":100,"proposer":"p","hash":"%s","qc":%s,"tc":null}`, name, qc))
			if err := m.processBlockRaw(block, "in"); err == nil {
				t.Fatalf("accepted invalid QC: %s", qc)
			}
			stats := m.GetVerificationStats()
			if stats.BlocksProcessed != 0 || stats.QCsProcessed != 0 || len(m.qcWindow) != 0 {
				t.Fatalf("invalid QC partially mutated state: stats=%+v window=%d", stats, len(m.qcWindow))
			}
			if got := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("in")); got != before {
				t.Fatal("invalid QC incremented direction event")
			}
		})
	}
}

func TestConsensusBlockDirectionEventsRemainDistinctWhileObservationDedupes(t *testing.T) {
	m := NewConsensusMonitor(&config.Config{})
	outBefore := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("out"))
	inBefore := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("in"))
	block := json.RawMessage(`{"round":42,"proposer":"0xaaaa..bbbb","hash":"0xblock","tc":null}`)
	if err := m.processBlockRaw(block, "out"); err != nil {
		t.Fatal(err)
	}
	if err := m.processBlockRaw(block, "in"); err != nil {
		t.Fatal(err)
	}
	if got := m.GetVerificationStats().BlocksProcessed; got != 1 {
		t.Fatalf("block observations = %d, want 1", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("out")) - outBefore; got != 1 {
		t.Fatalf("out direction delta = %v, want 1", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusBlockDirectionEvents.WithLabelValues("in")) - inBefore; got != 1 {
		t.Fatalf("in direction delta = %v, want 1", got)
	}
}

func TestConsensusBlockCountsOnlyDecodedNonNullTC(t *testing.T) {
	counter, err := otel.Meter("consensus-block-test").Int64Counter("test_tc_blocks")
	if err != nil {
		t.Fatal(err)
	}
	oldCounter := metrics.HLConsensusTCBlocksCounter
	metrics.HLConsensusTCBlocksCounter = counter
	t.Cleanup(func() { metrics.HLConsensusTCBlocksCounter = oldCounter })
	m := NewConsensusMonitor(&config.Config{})
	for i, tc := range []string{"", "null", `{"timeouts":[]}`} {
		tcField := ""
		if tc != "" {
			tcField = `,"tc":` + tc
		}
		block := json.RawMessage(`{"round":` + fmt.Sprint(100+i) + `,"proposer":"0xaaaa..bbbb","hash":"block-` + fmt.Sprint(i) + `"` + tcField + `}`)
		if err := m.processBlockRaw(block, "in"); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.GetVerificationStats().TCsProcessed; got != 1 {
		t.Fatalf("TC observations = %d, want only decoded object", got)
	}
	if err := m.processBlockRaw(json.RawMessage(`{"round":200,"proposer":"p","hash":"bad","tc":[]}`), "in"); err == nil {
		t.Fatal("non-null non-object TC was accepted")
	}
}

func TestConsensusProgressLagRequiresOrderedSameDomainOperands(t *testing.T) {
	m := NewConsensusMonitor(&config.Config{})
	metrics.HLConsensusLocalRoundLag.DeleteLabelValues("execution")
	t.Cleanup(func() { metrics.HLConsensusLocalRoundLag.DeleteLabelValues("execution") })
	now := time.Now()
	if err := m.processExecutionState(json.RawMessage(`{"round":10}`), now); err != nil {
		t.Fatal(err)
	}
	if validatorCollectorHasLabels(metrics.HLConsensusLocalRoundLag, map[string]string{"field": "execution"}) {
		t.Fatal("execution lag published without a consensus-round operand")
	}
	otherBefore := validatorMetricValue(t, metrics.HLConsensusRoundAdvanceEvents.WithLabelValues("other"))
	if err := m.processRoundAdvance(json.RawMessage(`{"prev_round":11,"round":12,"reason":"future_reason"}`), now); err != nil {
		t.Fatal(err)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusLocalRoundLag.WithLabelValues("execution")); got != 2 {
		t.Fatalf("execution lag = %v, want 2", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusRoundAdvanceEvents.WithLabelValues("other")) - otherBefore; got != 1 {
		t.Fatalf("bounded other reason delta = %v", got)
	}
	if err := m.processExecutionState(json.RawMessage(`{"round":13}`), now); err != nil {
		t.Fatal(err)
	}
	if validatorCollectorHasLabels(metrics.HLConsensusLocalRoundLag, map[string]string{"field": "execution"}) {
		t.Fatal("negative execution lag left a stale prior series")
	}
}

func TestConsensusVoteObservationAcceptsUnmappedSignerWithoutClaimingCoverage(t *testing.T) {
	m := NewConsensusMonitor(&config.Config{})
	before := validatorMetricValue(t, metrics.HLConsensusAcceptedVoteObservations)
	if err := m.processVoteStruct(&ConsensusVoteMessage{SignerId: "0xdead..beef", Round: 9}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusAcceptedVoteObservations) - before; got != 1 {
		t.Fatalf("accepted vote observation delta = %v", got)
	}
	if err := m.processVoteStruct(&ConsensusVoteMessage{SignerId: "0xdead..beef"}, time.Now()); err == nil {
		t.Fatal("roundless vote was accepted")
	}
}

func TestBlockTimePrecisionLagAndRequiredFields(t *testing.T) {
	previous := time.Unix(0, 0)
	current := previous.Add(500 * time.Microsecond)
	if got := blockIntervalMilliseconds(previous, current); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("sub-ms interval = %.12fms, want 0.5ms", got)
	}
	if lag, ok := beginWallReceiptLag(previous, current); !ok || lag != 0.0005 {
		t.Fatalf("begin-wall lag = %v, %v", lag, ok)
	}
	if _, ok := beginWallReceiptLag(current, previous); ok {
		t.Fatal("future begin-wall time was accepted")
	}
	for _, line := range []string{
		`{"block_time":"2026-08-08T00:00:00.000000000","apply_duration":0.1}`,
		`{"height":1,"block_time":"2026-08-08T00:00:00.000000000"}`,
	} {
		if err := parseBlockTimeLine(line, "fast"); err == nil {
			t.Fatalf("missing required field accepted: %s", line)
		}
	}
}

func TestParseStatusSnapshotPreservesNullZeroOmissionAndRounds(t *testing.T) {
	const signerOne = "0x1111111111111111111111111111111111111111"
	const signerTwo = "0x2222222222222222222222222222222222222222"
	line := []byte(`["2026-08-08T00:00:00.000000000",{` +
		`"round":100,"heartbeat_statuses":[` +
		`["` + signerOne + `",{"since_last_success":12.5,"last_ack_duration":0}],` +
		`["` + signerTwo + `",{"since_last_success":20,"last_ack_duration":null}]],` +
		`"disconnected_validators":[["` + signerOne + `",[["` + signerTwo + `",42]]]],` +
		`"validators_missing_heartbeat":["` + signerTwo + `"]}]`)
	snapshot, err := parseStatusSnapshot(line)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Round != 100 || len(snapshot.Heartbeats) != 2 || len(snapshot.Disconnected) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if !snapshot.Heartbeats[0].LastAckDuration.Present || snapshot.Heartbeats[0].LastAckDuration.Null || snapshot.Heartbeats[0].LastAckDuration.Value != 0 {
		t.Fatalf("numeric zero ack lost: %+v", snapshot.Heartbeats[0].LastAckDuration)
	}
	if !snapshot.Heartbeats[1].LastAckDuration.Present || !snapshot.Heartbeats[1].LastAckDuration.Null {
		t.Fatalf("null ack lost: %+v", snapshot.Heartbeats[1].LastAckDuration)
	}

	omitted, err := parseStatusSnapshot([]byte(`["2026-08-08T00:00:01.000000000",{"round":101}]`))
	if err != nil || omitted.HeartbeatFieldPresent || omitted.DisconnectedPresent {
		t.Fatalf("omitted fields = %+v, %v", omitted, err)
	}
	for _, invalid := range []string{
		`["2026-08-08T00:00:00.000000000",{"round":1,"heartbeat_statuses":null}]`,
		`["2026-08-08T00:00:00.000000000",{"round":1,"disconnected_validators":null}]`,
		`["2026-08-08T00:00:00.000000000",{"heartbeat_statuses":[]}]`,
		`["2026-08-08T00:00:00.000000000",{"round":1,"heartbeat_statuses":[["` + signerOne + `",{"last_ack_duration":-1}]]}]`,
		`["2026-08-08T00:00:00.000000000",{"round":1,"heartbeat_statuses":[["` + signerOne + `",{"since_last_success":null}]]}]`,
		`["2026-08-08T00:00:00.000000000",{"round":1,"disconnected_validators":[["` + signerOne + `",null]]}]`,
		`["2026-08-08T00:00:00.000000000",{"round":1,"disconnected_validators":[["` + signerOne + `",[["` + signerTwo + `",null]]]]}]`,
	} {
		if _, err := parseStatusSnapshot([]byte(invalid)); err == nil {
			t.Fatalf("invalid status accepted: %s", invalid)
		}
	}
}

func TestInvalidStatusScalarRetainsLastAcceptedTimestamp(t *testing.T) {
	m := NewConsensusMonitor(&config.Config{})
	last := time.Unix(1_800_000_000, 0)
	m.lastStatusSourceTime = last
	line := `["2026-08-08T00:00:00.000000000",{"round":1,"disconnected_validators":[["0x1111111111111111111111111111111111111111",[["0x2222222222222222222222222222222222222222",null]]]]}]`
	if err := m.processStatusLine(line); err == nil {
		t.Fatal("status with null since_round was accepted")
	}
	if !m.lastStatusSourceTime.Equal(last) {
		t.Fatalf("invalid status advanced accepted timestamp to %v", m.lastStatusSourceTime)
	}
}

func TestStatusSnapshotIdentityAndCardinalityBounds(t *testing.T) {
	address := func(i int) string { return fmt.Sprintf("0x%040x", i+1) }
	heartbeats := make([]any, validatorSummaryLimit+1)
	missing := make([]string, validatorSummaryLimit+1)
	reporters := make([]any, validatorSummaryLimit+1)
	for i := 0; i <= validatorSummaryLimit; i++ {
		heartbeats[i] = []any{address(i), map[string]any{"since_last_success": 1}}
		missing[i] = address(i)
		reporters[i] = []any{address(i), i}
	}
	encode := func(value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for name, field := range map[string]string{
		"heartbeat rows": `"heartbeat_statuses":` + encode(heartbeats),
		"missing rows":   `"validators_missing_heartbeat":` + encode(missing),
		"reporter rows":  `"disconnected_validators":[["` + address(validatorSummaryLimit+2) + `",` + encode(reporters) + `]]`,
	} {
		t.Run(name, func(t *testing.T) {
			line := `["2026-08-08T00:00:00.000000000",{"round":1,` + field + `}]`
			if _, err := parseStatusSnapshot([]byte(line)); err == nil {
				t.Fatalf("accepted over-cap %s", name)
			}
		})
	}

	valid := address(700)
	for name, field := range map[string]string{
		"heartbeat signer":  `"heartbeat_statuses":[["bad",{}]]`,
		"missing signer":    `"validators_missing_heartbeat":["bad"]`,
		"subject signer":    `"disconnected_validators":[["bad",[]]]`,
		"reporter signer":   `"disconnected_validators":[["` + valid + `",[["bad",1]]]]`,
		"duplicate subject": `"disconnected_validators":[["` + valid + `",[["` + address(701) + `",1]]],["` + valid + `",[["` + address(702) + `",2]]]]`,
	} {
		t.Run(name, func(t *testing.T) {
			line := `["2026-08-08T00:00:00.000000000",{"round":1,` + field + `}]`
			if _, err := parseStatusSnapshot([]byte(line)); err == nil {
				t.Fatalf("accepted invalid %s", name)
			}
		})
	}

	full := "0xabcdef1111111111111111111111111111111234"
	if _, err := parseStatusHeartbeats(json.RawMessage(`[["` + full + `",{}],["0xabcd..1234",{}]]`)); err == nil {
		t.Fatal("full/truncated alias duplicate was accepted")
	}

	// The wire form carries only the first and last four hex digits. Distinct
	// full addresses with the same fingerprint remain distinct identities;
	// only a mixed full/truncated spelling is ambiguous and rejected.
	fullA := fmt.Sprintf("0xabcd%032x1234", 1)
	fullB := fmt.Sprintf("0xabcd%032x1234", 2)
	if wireAddressKey(fullA) != wireAddressKey(fullB) || fullA == fullB {
		t.Fatal("test addresses do not exercise a wire-key collision")
	}
	collidingFulls := []any{
		fullA,
		fullB,
	}
	collidingHeartbeats := []any{
		[]any{fullA, map[string]any{"since_last_success": 1}},
		[]any{fullB, map[string]any{"since_last_success": 2}},
	}
	collidingDisconnected := []any{
		[]any{fullA, []any{[]any{address(800), 1}}},
		[]any{fullB, []any{[]any{address(801), 2}}},
	}
	line := `["2026-08-08T00:00:00.000000000",{"round":1,"heartbeat_statuses":` + encode(collidingHeartbeats) +
		`,"validators_missing_heartbeat":` + encode(collidingFulls) +
		`,"disconnected_validators":` + encode(collidingDisconnected) + `}]`
	if _, err := parseStatusSnapshot([]byte(line)); err != nil {
		t.Fatalf("distinct full addresses sharing one wire key were rejected: %v", err)
	}
}

func TestHeartbeatJoinKeyIsStableAcrossAddressCachePopulation(t *testing.T) {
	sentCounter, err := otel.Meter("consensus-heartbeat-cache-order-test").Int64Counter("test_heartbeat_cache_order_sent")
	if err != nil {
		t.Fatal(err)
	}
	oldSentCounter := metrics.HLConsensusHeartbeatSentCounter
	metrics.HLConsensusHeartbeatSentCounter = sentCounter
	metrics.ClearAddressCache()
	t.Cleanup(func() {
		metrics.HLConsensusHeartbeatSentCounter = oldSentCounter
		metrics.ClearAddressCache()
	})

	const full = "0x3333333333333333333333333333333333333333"
	const truncated = "0x3333..3333"
	m := NewConsensusMonitor(&config.Config{})
	base := time.Now()
	if err := m.processHeartbeatOut(&HeartbeatMessage{Validator: truncated, RandomID: 900, Round: 77}, base); err != nil {
		t.Fatal(err)
	}
	metrics.RegisterFullAddress(full)
	before := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("self", "matched"))
	if err := m.processHeartbeatAck(&HeartbeatAckMessage{Validator: full, RandomID: 900, Round: 77}, full, base.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("self", "matched")) - before; got != 1 {
		t.Fatalf("cache-order-stable self join delta = %v, want 1", got)
	}
}

func TestWireAddressesConflictUsesEverySuppliedPrefixDigit(t *testing.T) {
	fullA := fmt.Sprintf("0xabcde%031x1234", 1)
	fullB := fmt.Sprintf("0xabcdf%031x1234", 2)
	for name, tc := range map[string]struct {
		left, right string
		want        bool
	}{
		"exact":                 {"0xabcde..1234", "0xabcde..1234", true},
		"overlapping prefixes":  {"0xabcd..1234", "0xabcde..1234", true},
		"distinct prefixes":     {"0xabcde..1234", "0xabcdf..1234", false},
		"full matching prefix":  {fullA, "0xabcde..1234", true},
		"full distinct prefix":  {fullA, "0xabcdf..1234", false},
		"distinct full address": {fullA, fullB, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := wireAddressesConflict(tc.left, tc.right); got != tc.want {
				t.Fatalf("wireAddressesConflict(%q, %q) = %t, want %t", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestEligibleStatusSummariesRequireFreshAPIGeneration(t *testing.T) {
	metrics.HLConsensusStatusEligibleSummary.Reset()
	t.Cleanup(metrics.HLConsensusStatusEligibleSummary.Reset)

	const signer = "0x1111111111111111111111111111111111111111"
	snapshot := statusSnapshot{
		MissingHeartbeatPresent: true,
		MissingHeartbeatSigners: []string{signer},
		DisconnectedPresent:     true,
		Disconnected: []statusDisconnectedPair{{
			SubjectSigner:  signer,
			ReporterSigner: "0x2222222222222222222222222222222222222222",
		}},
	}
	now := time.Unix(1_800_000_000, 0)
	eligible := []metrics.EligibleValidator{{Signer: signer}}

	publishEligibleStatusSummariesAt(snapshot, eligible, time.Time{}, now)
	if rows := b03CollectorRows(t, metrics.HLConsensusStatusEligibleSummary); len(rows) != 0 {
		t.Fatalf("missing API generation published eligible zeroes: %d rows", len(rows))
	}

	publishEligibleStatusSummariesAt(snapshot, eligible, now, now)
	for _, state := range []string{"missing_heartbeat", "disconnected"} {
		value, ok := b03CollectorValue(t, metrics.HLConsensusStatusEligibleSummary, map[string]string{"state": state})
		if !ok || value != 1 {
			t.Fatalf("fresh API state %s = %v present=%v, want 1", state, value, ok)
		}
	}

	publishEligibleStatusSummariesAt(snapshot, eligible, now, now.Add(validatorAPITargetFreshness+time.Second))
	if rows := b03CollectorRows(t, metrics.HLConsensusStatusEligibleSummary); len(rows) != 0 {
		t.Fatalf("stale API generation left eligible summaries: %d rows", len(rows))
	}

	publishEligibleStatusSummariesAt(snapshot, eligible, now.Add(2*time.Minute), now)
	if rows := b03CollectorRows(t, metrics.HLConsensusStatusEligibleSummary); len(rows) != 0 {
		t.Fatalf("future API generation published eligible summaries: %d rows", len(rows))
	}
}

func TestValidatorAPICommitAndFreshnessRecomputeRetainedStatusJoin(t *testing.T) {
	oldJailedPrev := jailedLocalPrev
	oldJailedCurrent := append([]string(nil), jailedLocalCurrent...)
	metrics.HLConsensusStatusEligibleSummary.Reset()
	metrics.HLConsensusValidatorJailedLocal.Reset()
	jailedLocalPrev = make(map[string][3]string)
	jailedLocalCurrent = nil
	clearLatestEligibleStatusSnapshot()
	metrics.ReplaceValidatorSnapshot(nil)
	t.Cleanup(func() {
		metrics.HLConsensusStatusEligibleSummary.Reset()
		metrics.HLConsensusValidatorJailedLocal.Reset()
		jailedLocalPrev = oldJailedPrev
		jailedLocalCurrent = oldJailedCurrent
		clearLatestEligibleStatusSnapshot()
		metrics.ReplaceValidatorSnapshot(nil)
	})

	const validator = "0x1111111111111111111111111111111111111111"
	const signer = "0x2222222222222222222222222222222222222222"
	snapshot := statusSnapshot{
		HeartbeatFieldPresent: true,
		Heartbeats: []statusHeartbeat{{
			Signer: signer,
			SinceLastSuccess: optionalStatusFloat{
				Present: true, Value: 1,
			},
		}},
		MissingHeartbeatPresent: true,
		MissingHeartbeatSigners: []string{signer},
		DisconnectedPresent:     true,
		Disconnected: []statusDisconnectedPair{{
			SubjectSigner: signer, ReporterSigner: signer, SinceRound: 1,
		}},
	}
	metrics.WithPrometheusSnapshotUpdate(func() {
		replaceLatestEligibleStatusSnapshot(snapshot)
		publishConsensusStatusDetailUnlocked(snapshot)
		publishJailedLocal([]string{signer})
	})
	if !validatorCollectorHasLabels(metrics.HLConsensusValidatorJailedLocal, map[string]string{
		"validator": "unknown", "signer": signer, "name": "unknown",
	}) {
		t.Fatal("pre-API jailed-local row did not retain explicit unknown identity")
	}
	now := time.Now()
	apiRows := []metrics.ValidatorSummarySnapshot{{
		Validator: validator, Signer: signer, Name: "validator", Active: true,
	}}
	commitValidatorAPISnapshot(nil, apiRows, now)
	if validatorCollectorHasLabels(metrics.HLConsensusValidatorJailedLocal, map[string]string{
		"validator": "unknown", "signer": signer, "name": "unknown",
	}) {
		t.Fatal("API commit retained stale unknown jailed-local labels")
	}
	if !validatorCollectorHasLabels(metrics.HLConsensusValidatorJailedLocal, map[string]string{
		"validator": validator, "signer": signer, "name": "validator",
	}) {
		t.Fatal("API commit did not re-enrich retained jailed-local row")
	}
	for _, state := range []string{"missing_heartbeat", "disconnected"} {
		value, ok := b03CollectorValue(t, metrics.HLConsensusStatusEligibleSummary, map[string]string{"state": state})
		if !ok || value != 1 {
			t.Fatalf("API commit did not refresh retained %s join: value=%v present=%v", state, value, ok)
		}
	}

	commitValidatorAPISnapshot(nil, []metrics.ValidatorSummarySnapshot{}, now.Add(time.Second))
	for _, state := range []string{"missing_heartbeat", "disconnected"} {
		value, ok := b03CollectorValue(t, metrics.HLConsensusStatusEligibleSummary, map[string]string{"state": state})
		if !ok || value != 0 {
			t.Fatalf("API membership removal did not refresh %s join: value=%v present=%v", state, value, ok)
		}
	}

	metrics.ReplaceValidatorSnapshot(apiRows)
	refreshEligibleStatusSummariesAt(time.Now().Add(validatorAPITargetFreshness + time.Second))
	if rows := b03CollectorRows(t, metrics.HLConsensusStatusEligibleSummary); len(rows) != 0 {
		t.Fatalf("freshness expiry retained %d eligible status rows", len(rows))
	}
}

func TestHeartbeatJoinsSeparatePeerSelfDuplicateMismatchAndExpiry(t *testing.T) {
	sentCounter, err := otel.Meter("consensus-heartbeat-test").Int64Counter("test_heartbeat_sent")
	if err != nil {
		t.Fatal(err)
	}
	oldSentCounter := metrics.HLConsensusHeartbeatSentCounter
	metrics.HLConsensusHeartbeatSentCounter = sentCounter
	t.Cleanup(func() { metrics.HLConsensusHeartbeatSentCounter = oldSentCounter })
	m := NewConsensusMonitor(&config.Config{})
	base := time.Now()
	local := "0x1111111111111111111111111111111111111111"
	peer := "0x2222222222222222222222222222222222222222"
	matchedPeerBefore := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("peer", "matched"))
	matchedSelfBefore := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("self", "matched"))
	mismatchPeerBefore := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("peer", "mismatch"))
	mismatchUnknownBefore := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("unknown", "mismatch"))
	expiredBefore := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("unknown", "expired"))
	peerHistBefore := validatorHistogramCount(t, metrics.HLConsensusHeartbeatPeerAckDelay)
	selfHistBefore := validatorHistogramCount(t, metrics.HLConsensusSelfHeartbeatLoopDuration)

	hb := &HeartbeatMessage{Validator: local, RandomID: 424, Round: 99}
	ack := &HeartbeatAckMessage{Validator: local, RandomID: 424, Round: 99}
	if err := m.processHeartbeatOut(hb, base); err != nil {
		t.Fatal(err)
	}
	if err := m.processHeartbeatAck(ack, peer, base.Add(10*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := m.processHeartbeatAck(ack, local, base.Add(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := m.processHeartbeatAck(ack, peer, base.Add(30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	wrongRound := &HeartbeatAckMessage{Validator: local, RandomID: 424, Round: 100}
	if err := m.processHeartbeatAck(wrongRound, peer, base.Add(40*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	// One unmatched heartbeat and the already-matched heartbeat both expire;
	// only the unmatched join is an expired failure.
	if err := m.processHeartbeatOut(&HeartbeatMessage{Validator: local, RandomID: 425, Round: 99}, base); err != nil {
		t.Fatal(err)
	}
	if err := m.processHeartbeatOut(&HeartbeatMessage{Validator: local, RandomID: 426, Round: 101}, base.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if got := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("peer", "matched")) - matchedPeerBefore; got != 1 {
		t.Fatalf("peer matched delta = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("self", "matched")) - matchedSelfBefore; got != 1 {
		t.Fatalf("self matched delta = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("peer", "mismatch")) - mismatchPeerBefore; got != 1 {
		t.Fatalf("duplicate peer delta = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("unknown", "mismatch")) - mismatchUnknownBefore; got != 1 {
		t.Fatalf("wrong-round mismatch delta = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusHeartbeatJoin.WithLabelValues("unknown", "expired")) - expiredBefore; got != 1 {
		t.Fatalf("unmatched expiry delta = %v", got)
	}
	if got := validatorHistogramCount(t, metrics.HLConsensusHeartbeatPeerAckDelay) - peerHistBefore; got != 1 {
		t.Fatalf("peer histogram delta = %d", got)
	}
	if got := validatorHistogramCount(t, metrics.HLConsensusSelfHeartbeatLoopDuration) - selfHistBefore; got != 1 {
		t.Fatalf("self histogram delta = %d", got)
	}
	if !validatorCollectorHasLabels(metrics.HLConsensusHeartbeatPeerAcks, map[string]string{
		"validator": "unknown", "signer": "unknown", "name": "unknown",
	}) {
		t.Fatal("unmapped peer acknowledgement was not aggregated into the bounded unknown series")
	}
	if err := m.processHeartbeatOut(&HeartbeatMessage{Validator: "not-an-address", RandomID: 500, Round: 101}, base); err == nil {
		t.Fatal("heartbeat accepted an invalid validator identity")
	}
	if err := m.processHeartbeatAck(ack, "not-an-address", base.Add(time.Second)); err == nil {
		t.Fatal("heartbeat acknowledgement accepted an invalid source identity")
	}
}

func TestHeartbeatPeerAckDetailIsBoundedAcrossUnknownAndKnownChurn(t *testing.T) {
	heartbeatPeerAckSeries.Lock()
	oldSeen := heartbeatPeerAckSeries.seen
	heartbeatPeerAckSeries.seen = make(map[[3]string]struct{}, validatorSummaryLimit)
	heartbeatPeerAckSeries.Unlock()
	metrics.HLConsensusHeartbeatPeerAcks.Reset()
	t.Cleanup(func() {
		heartbeatPeerAckSeries.Lock()
		heartbeatPeerAckSeries.seen = oldSeen
		heartbeatPeerAckSeries.Unlock()
		metrics.HLConsensusHeartbeatPeerAcks.Reset()
	})

	m := NewConsensusMonitor(&config.Config{})
	base := time.Now()
	local := "0xffffffffffffffffffffffffffffffffffffffff"
	key := heartbeatKey{validator: wireAddressKey(local), randomID: 700, round: 100}
	m.heartbeats[key] = heartbeatInfo{timestamp: base}
	ack := &HeartbeatAckMessage{Validator: local, RandomID: 700, Round: 100}
	for i := 0; i < 100; i++ {
		source := fmt.Sprintf("0x%040x", i+1)
		if err := m.processHeartbeatAck(ack, source, base.Add(time.Duration(i+1)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if rows := b03CollectorRows(t, metrics.HLConsensusHeartbeatPeerAcks); len(rows) != 1 {
		t.Fatalf("100 unknown peer identities produced %d detailed rows, want 1", len(rows))
	}

	for i := 0; i < validatorSummaryLimit; i++ {
		labels := [3]string{fmt.Sprintf("validator-%d", i), fmt.Sprintf("signer-%d", i), fmt.Sprintf("name-%d", i)}
		if i == 0 {
			// The unknown row above already occupies one sticky slot.
			continue
		}
		if !admitHeartbeatPeerAckSeries(labels) {
			t.Fatalf("series %d rejected before cap", i)
		}
	}
	if admitHeartbeatPeerAckSeries([3]string{"overflow", "overflow", "overflow"}) {
		t.Fatal("peer acknowledgement detail admitted a series beyond the lifetime cap")
	}
	if !admitHeartbeatPeerAckSeries([3]string{"validator-1", "signer-1", "name-1"}) {
		t.Fatal("existing peer acknowledgement detail row was rejected at cap")
	}
}

func TestParseValidatorEMASnapshotInitializationSentinelAndMixedZero(t *testing.T) {
	timestamp := `"2026-08-08T00:00:00.000000000"`
	_, state, values, err := parseValidatorEMASnapshot([]byte(`[` + timestamp + `,[["a",0],["b",0]]]`))
	if err != nil || state != "initializing" || len(values) != 0 {
		t.Fatalf("all-zero EMA = %q %v %v", state, values, err)
	}
	_, state, values, err = parseValidatorEMASnapshot([]byte(`[` + timestamp + `,[["a",0],["b",0.3999999999],["c",0.41]]]`))
	if err != nil || state != "measured" || len(values) != 2 || values["a"] != 0 || values["c"] != 0.41 {
		t.Fatalf("mixed EMA = %q %v %v", state, values, err)
	}
	if _, _, _, err := parseValidatorEMASnapshot([]byte(`[` + timestamp + `,null]`)); err == nil {
		t.Fatal("null EMA rows accepted as empty snapshot")
	}
}

func TestParseValidatorConnectionUsesOnlyBoundedClasses(t *testing.T) {
	known, err := parseValidatorConnectionLine([]byte(`["2026-08-08T00:00:00.000000000",["handle_stream_connection","203.0.113.7:4003","validator"]]`))
	if err != nil || !known.known || known.event != "handle_stream_connection" || known.result != "observed" || known.subsystem != "validator" {
		t.Fatalf("known observation = %+v, %v", known, err)
	}
	unknown, err := parseValidatorConnectionLine([]byte(`["2026-08-08T00:00:00.000000000",["future_tag","sensitive-session","203.0.113.8"]]`))
	if err != nil || unknown.known || unknown.event != "other" || unknown.result != "other" || unknown.subsystem != "other" {
		t.Fatalf("unknown observation = %+v, %v", unknown, err)
	}
}

func TestValidatorProbeTargetsRequireFreshEligibleAPIAndProfileIntersection(t *testing.T) {
	metrics.ReplaceValidatorSnapshot([]metrics.ValidatorSummarySnapshot{
		{Validator: "eligible", Signer: "eligible-signer", Name: "eligible", Stake: 3, Active: true},
		{Validator: "jailed", Signer: "jailed-signer", Name: "jailed", Stake: 2, Active: true, Jailed: true},
		{Validator: "inactive", Signer: "inactive-signer", Name: "inactive", Stake: 1},
	})
	t.Cleanup(func() { metrics.ReplaceValidatorSnapshot(nil) })
	now := time.Now()
	validatorIPMutex.Lock()
	validatorDataByAddress = map[string]validatorData{
		"eligible": {IP: "192.0.2.10", Moniker: "eligible", LastSeen: now},
		"jailed":   {IP: "192.0.2.11", Moniker: "jailed", LastSeen: now},
		"inactive": {IP: "192.0.2.12", Moniker: "inactive", LastSeen: now},
	}
	validatorIPMutex.Unlock()
	t.Cleanup(func() {
		validatorIPMutex.Lock()
		validatorDataByAddress = make(map[string]validatorData)
		validatorIPMutex.Unlock()
	})

	targets := currentValidatorProbeTargets(now, validatorSummaryLimit)
	if len(targets) != 1 || targets[0].identity.Validator != "eligible" {
		t.Fatalf("probe targets = %+v, want only API-active-and-unjailed", targets)
	}
	if stale := currentValidatorProbeTargets(now.Add(validatorAPITargetFreshness+time.Second), validatorSummaryLimit); len(stale) != 0 {
		t.Fatalf("stale API generation still probed: %+v", stale)
	}
}

func validatorMetricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var row dto.Metric
	if err := metric.Write(&row); err != nil {
		t.Fatal(err)
	}
	if row.Counter != nil {
		return row.GetCounter().GetValue()
	}
	if row.Gauge != nil {
		return row.GetGauge().GetValue()
	}
	t.Fatalf("unsupported metric type: %T", metric)
	return 0
}

func validatorHistogramCount(t *testing.T, metric prometheus.Metric) uint64 {
	t.Helper()
	var row dto.Metric
	if err := metric.Write(&row); err != nil {
		t.Fatal(err)
	}
	if row.Histogram == nil {
		t.Fatalf("metric is not a histogram: %T", metric)
	}
	return row.Histogram.GetSampleCount()
}

func validatorCollectorHasLabels(collector prometheus.Collector, want map[string]string) bool {
	ch := make(chan prometheus.Metric, 1024)
	collector.Collect(ch)
	close(ch)
	for metric := range ch {
		var row dto.Metric
		if metric.Write(&row) != nil || len(row.Label) != len(want) {
			continue
		}
		matched := true
		for _, label := range row.Label {
			if want[label.GetName()] != label.GetValue() {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func validatorCollectorContainsLabelValue(collector prometheus.Collector, want string) bool {
	ch := make(chan prometheus.Metric, 1024)
	collector.Collect(ch)
	close(ch)
	for metric := range ch {
		var row dto.Metric
		if metric.Write(&row) != nil {
			continue
		}
		for _, label := range row.Label {
			if label.GetValue() == want {
				return true
			}
		}
	}
	return false
}
