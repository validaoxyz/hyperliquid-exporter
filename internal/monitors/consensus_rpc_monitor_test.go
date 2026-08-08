package monitors

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	rpcTestTimestamp = "2026-08-08T00:00:00.000000000"
	rpcTestRequest   = `{"content":{"BlocksAndTxs":{"after_round":10,"until_block_hash":"0xfabricated"}},"query_peers":false}`
	rpcTestResponse  = `{"Ok":{"BlocksAndTxs":{"after_round":10,"until_block_hash":"0xfabricated","n":102,"last_block_hash":"0xresult"}}}`
)

func TestConsensusRPCCompleteLifecyclePublishesStagesAndServedQuantityOnce(t *testing.T) {
	tracker := newConsensusRPCTracker()
	stages := []struct {
		stage, content string
	}{
		{"stream_opened", "other"},
		{"request_received", "blocks_and_txs"},
		{"task_inbound", "blocks_and_txs"},
		{"task_response", "blocks_and_txs"},
	}
	stageBefore := make([]float64, len(stages))
	for i, stage := range stages {
		stageBefore[i] = validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("serve", stage.stage, "observed", stage.content))
	}
	terminalBefore := validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("serve", "response_sent", "ok", "blocks_and_txs"))
	blocksBefore := validatorMetricValue(t, metrics.HLConsensusRPCBlocksServed)

	prefix := []string{
		rpcLine(`["Incoming tcp stream","session-secret","203.0.113.1:1234"]`),
		rpcLine(`["Received rpc request","session-secret",` + rpcTestRequest + `]`),
		rpcLine(`["Rpc task inbound",` + rpcTestRequest + `]`),
		rpcLine(`["Rpc task response",` + rpcTestRequest + `,` + rpcTestResponse + `]`),
	}
	for _, line := range prefix {
		record, err := parseConsensusRPCLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if got := tracker.accept(record); got != "ok" {
			t.Fatalf("prefix accept = %q", got)
		}
	}
	for i, stage := range stages {
		if got := validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("serve", stage.stage, "observed", stage.content)); got != stageBefore[i] {
			t.Fatalf("incomplete lifecycle published %s", stage.stage)
		}
	}

	terminal, err := parseConsensusRPCLine([]byte(rpcLine(`["Outbound response","session-secret",` + rpcTestRequest + `,` + rpcTestResponse + `]`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := tracker.accept(terminal); got != "ok" {
		t.Fatalf("terminal accept = %q", got)
	}
	for i, stage := range stages {
		if got := validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("serve", stage.stage, "observed", stage.content)) - stageBefore[i]; got != 1 {
			t.Fatalf("%s delta = %v, want 1", stage.stage, got)
		}
	}
	if got := validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("serve", "response_sent", "ok", "blocks_and_txs")) - terminalBefore; got != 1 {
		t.Fatalf("terminal delta = %v, want 1", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusRPCBlocksServed) - blocksBefore; got != 102 {
		t.Fatalf("served block delta = %v, want 102", got)
	}
	if got := tracker.accept(terminal); got != "unjoinable" {
		t.Fatalf("replayed terminal = %q, want unjoinable", got)
	}
}

func TestConsensusRPCAcceptsPositiveBoundedServedCounts(t *testing.T) {
	for _, n := range []string{"1", "100", "102", fmt.Sprintf("%d", maxConsensusRPCServedBlocks)} {
		response := fmt.Sprintf(`{"Ok":{"BlocksAndTxs":{"n":%s}}}`, n)
		if _, outcome, content, _, err := parseConsensusRPCResponse(json.RawMessage(response)); err != nil || outcome != "ok" || content != "blocks_and_txs" {
			t.Fatalf("served n=%s rejected: outcome=%q content=%q err=%v", n, outcome, content, err)
		}
	}
}

func TestConsensusRPCRejectsInvalidServedCountsAndQueryPeers(t *testing.T) {
	for _, n := range []string{"0", "-1", "2.5", "2.0", "null", fmt.Sprintf("%d", maxConsensusRPCServedBlocks+1), "9223372036854775808"} {
		response := fmt.Sprintf(`{"Ok":{"BlocksAndTxs":{"n":%s}}}`, n)
		line := rpcLine(`["Rpc task response",` + rpcTestRequest + `,` + response + `]`)
		if _, err := parseConsensusRPCLine([]byte(line)); err == nil {
			t.Fatalf("served n=%s accepted", n)
		}
	}
	if _, err := parseConsensusRPCLine([]byte(rpcLine(`["Rpc task response",` + rpcTestRequest + `,{"Ok":{"BlocksAndTxs":{}}}]`))); err == nil {
		t.Fatal("missing served count accepted")
	}

	tracker := newConsensusRPCTracker()
	incoming, _ := parseConsensusRPCLine([]byte(rpcLine(`["Incoming tcp stream","session","203.0.113.1:1"]`)))
	if got := tracker.accept(incoming); got != "ok" {
		t.Fatalf("incoming = %q", got)
	}
	queryPeers := `{"content":{"BlocksAndTxs":{}},"query_peers":true}`
	received, err := parseConsensusRPCLine([]byte(rpcLine(`["Received rpc request","session",` + queryPeers + `]`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := tracker.accept(received); got != "unjoinable" {
		t.Fatalf("query_peers=true = %q, want unjoinable", got)
	}

	nullQueryPeers := `{"content":{"BlocksAndTxs":{}},"query_peers":null}`
	if _, err := parseConsensusRPCLine([]byte(rpcLine(`["Rpc task inbound",` + nullQueryPeers + `]`))); err == nil {
		t.Fatal("query_peers=null was accepted as false")
	}
}

func TestConsensusRPCUnknownTagsAndVariantsStayBoundedAndOpaque(t *testing.T) {
	tracker := newConsensusRPCTracker()
	before := validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("other", "other", "other", "other"))
	record, err := parseConsensusRPCLine([]byte(rpcLine(`[
		"future tag","203.0.113.99:4003","session-secret","0xfeedbeef"]`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := tracker.accept(record); got != "unknown_tag" {
		t.Fatalf("unknown tag result = %q", got)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusRPCEvents.WithLabelValues("other", "other", "other", "other")) - before; got != 1 {
		t.Fatalf("bounded other tuple delta = %v", got)
	}
	for _, sensitive := range []string{"203.0.113.99:4003", "session-secret", "0xfeedbeef"} {
		if validatorCollectorContainsLabelValue(metrics.HLConsensusRPCEvents, sensitive) {
			t.Fatalf("sensitive payload escaped into label: %q", sensitive)
		}
	}
	_, outcome, content, blocks, err := parseConsensusRPCResponse(json.RawMessage(`{"FutureVariant":{"opaque":"secret"}}`))
	if err != nil || outcome != "other" || content != "other" || blocks != 0 {
		t.Fatalf("unknown response variant = outcome=%q content=%q blocks=%d err=%v", outcome, content, blocks, err)
	}
}

func TestConsensusRPCCompletedJoinSetIsBounded(t *testing.T) {
	tracker := newConsensusRPCTracker()
	tracker.max = 1
	base := time.Now()
	var firstCompletion [32]byte
	for i := byte(1); i <= 2; i++ {
		var session, request, response [32]byte
		session[0], request[0], response[0] = i, i+10, i+20
		tracker.bySession[session] = &consensusRPCLifecycle{
			session: session, request: request, content: "blocks_and_txs",
			received: true, taskInbound: true, taskResponse: true,
			response: response, outcome: "ok", blocks: 2, updatedAt: base.Add(time.Duration(i) * time.Second),
		}
		tracker.byRequest[request] = session
		record := consensusRPCRecord{
			sourceTime: base.Add(time.Duration(i) * time.Second), tag: "Outbound response",
			session: session, request: request, response: response,
			content: "blocks_and_txs", outcome: "ok", blocks: 2,
		}
		if got := tracker.accept(record); got != "ok" {
			t.Fatalf("completion %d = %q", i, got)
		}
		completion := sha256.Sum256(append(session[:], response[:]...))
		if i == 1 {
			firstCompletion = completion
		}
	}
	if len(tracker.completed) != 1 {
		t.Fatalf("completed set size = %d, want 1", len(tracker.completed))
	}
	if _, exists := tracker.completed[firstCompletion]; exists {
		t.Fatal("oldest completion was not evicted at capacity")
	}
}

func rpcLine(payload string) string {
	return fmt.Sprintf(`[%q,%s]`, rpcTestTimestamp, payload)
}
