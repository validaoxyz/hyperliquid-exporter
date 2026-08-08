package monitors

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

type consensusRPCRecord struct {
	sourceTime  time.Time
	tag         string
	session     [32]byte
	request     [32]byte
	response    [32]byte
	content     string
	outcome     string
	blocks      int64
	queryPeers  bool
	hasSession  bool
	hasRequest  bool
	hasResponse bool
}

type consensusRPCLifecycle struct {
	session      [32]byte
	request      [32]byte
	content      string
	received     bool
	taskInbound  bool
	taskResponse bool
	response     [32]byte
	outcome      string
	blocks       int64
	updatedAt    time.Time
}

type consensusRPCTracker struct {
	bySession map[[32]byte]*consensusRPCLifecycle
	byRequest map[[32]byte][32]byte
	completed map[[32]byte]time.Time
	max       int
	ttl       time.Duration
}

func newConsensusRPCTracker() *consensusRPCTracker {
	return &consensusRPCTracker{
		bySession: make(map[[32]byte]*consensusRPCLifecycle),
		byRequest: make(map[[32]byte][32]byte),
		completed: make(map[[32]byte]time.Time),
		max:       4096,
		ttl:       10 * time.Minute,
	}
}

func monitorConsensusRPCLogs(ctx context.Context, cfg *config.Config, _ chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "consensus_rpc", "hourly")
	tracker := newConsensusRPCTracker()
	tailStream(ctx, tailStreamOpts{
		component: "consensus",
		name:      "consensus RPC stream",
		resolve: func() (string, error) {
			metrics.MarkSourceAttempt(metrics.SourceConsensusRPC)
			if _, err := os.Stat(root); err != nil {
				if os.IsNotExist(err) {
					metrics.MarkSourceAbsent(metrics.SourceConsensusRPC)
				} else {
					metrics.MarkSourceError(metrics.SourceConsensusRPC, metrics.SourceFailureStat)
				}
				return "", err
			}
			path, err := latestHourlyFile(root)
			if err != nil {
				metrics.MarkSourceError(metrics.SourceConsensusRPC, metrics.SourceFailureDiscovery)
			}
			return path, err
		},
		rescanEvery: 5 * time.Second,
		eofSleep:    250 * time.Millisecond,
		bufSize:     1 << 20,
		onSwitch:    func(string) { metrics.MarkSourceAvailable(metrics.SourceConsensusRPC) },
		onLine: func(line string) {
			record, err := parseConsensusRPCLine([]byte(strings.TrimSpace(line)))
			if err != nil {
				metrics.HLConsensusRPCParse.WithLabelValues("malformed").Inc()
				metrics.MarkSourceError(metrics.SourceConsensusRPC, metrics.SourceFailureSchema)
				return
			}
			result := tracker.accept(record)
			metrics.HLConsensusRPCParse.WithLabelValues(result).Inc()
			if result == "ok" || result == "unknown_tag" || result == "ignored_known" {
				metrics.MarkSourceValidObservation(metrics.SourceConsensusRPC, record.sourceTime)
				metrics.MarkSourcePublication(metrics.SourceConsensusRPC)
			}
		},
	})
}

func parseConsensusRPCLine(line []byte) (consensusRPCRecord, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return consensusRPCRecord{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if len(outer) != 2 {
		return consensusRPCRecord{}, fmt.Errorf("envelope length %d", len(outer))
	}
	var timestamp string
	if err := json.Unmarshal(outer[0], &timestamp); err != nil {
		return consensusRPCRecord{}, fmt.Errorf("unmarshal timestamp: %w", err)
	}
	sourceTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
	if err != nil {
		return consensusRPCRecord{}, fmt.Errorf("parse timestamp: %w", err)
	}
	var payload []json.RawMessage
	if err := json.Unmarshal(outer[1], &payload); err != nil || len(payload) == 0 {
		return consensusRPCRecord{}, fmt.Errorf("invalid payload")
	}
	var tag string
	if err := json.Unmarshal(payload[0], &tag); err != nil || tag == "" {
		return consensusRPCRecord{}, fmt.Errorf("invalid tag")
	}
	record := consensusRPCRecord{sourceTime: sourceTime, tag: tag, content: "other", outcome: "observed"}

	sessionAt := func(index int) error {
		if len(payload) <= index {
			return fmt.Errorf("missing session")
		}
		var session string
		if err := json.Unmarshal(payload[index], &session); err != nil || session == "" {
			return fmt.Errorf("invalid session")
		}
		record.session = sha256.Sum256([]byte(session))
		record.hasSession = true
		return nil
	}
	requestAt := func(index int) error {
		if len(payload) <= index {
			return fmt.Errorf("missing request")
		}
		digest, content, queryPeers, err := parseConsensusRPCRequest(payload[index])
		if err != nil {
			return err
		}
		record.request, record.content, record.queryPeers = digest, content, queryPeers
		record.hasRequest = true
		return nil
	}
	responseAt := func(index int) error {
		if len(payload) <= index {
			return fmt.Errorf("missing response")
		}
		digest, outcome, content, blocks, err := parseConsensusRPCResponse(payload[index])
		if err != nil {
			return err
		}
		record.response, record.outcome, record.blocks = digest, outcome, blocks
		if content != "other" {
			record.content = content
		}
		record.hasResponse = true
		return nil
	}

	switch tag {
	case "Incoming tcp stream":
		if len(payload) != 3 {
			return consensusRPCRecord{}, fmt.Errorf("incoming stream shape")
		}
		if err := sessionAt(1); err != nil {
			return consensusRPCRecord{}, err
		}
		var endpoint string
		if err := json.Unmarshal(payload[2], &endpoint); err != nil || endpoint == "" {
			return consensusRPCRecord{}, fmt.Errorf("invalid incoming endpoint")
		}
	case "Received rpc request":
		if len(payload) != 3 {
			return consensusRPCRecord{}, fmt.Errorf("received request shape")
		}
		if err := sessionAt(1); err != nil {
			return consensusRPCRecord{}, err
		}
		if err := requestAt(2); err != nil {
			return consensusRPCRecord{}, err
		}
	case "Rpc task inbound":
		if len(payload) != 2 {
			return consensusRPCRecord{}, fmt.Errorf("task inbound shape")
		}
		if err := requestAt(1); err != nil {
			return consensusRPCRecord{}, err
		}
	case "Rpc task response":
		if len(payload) != 3 {
			return consensusRPCRecord{}, fmt.Errorf("task response shape")
		}
		if err := requestAt(1); err != nil {
			return consensusRPCRecord{}, err
		}
		if err := responseAt(2); err != nil {
			return consensusRPCRecord{}, err
		}
	case "Outbound response":
		if len(payload) != 4 {
			return consensusRPCRecord{}, fmt.Errorf("outbound response shape")
		}
		if err := sessionAt(1); err != nil {
			return consensusRPCRecord{}, err
		}
		if err := requestAt(2); err != nil {
			return consensusRPCRecord{}, err
		}
		if err := responseAt(3); err != nil {
			return consensusRPCRecord{}, err
		}
	case "Rpc task size stats":
		// Structurally valid but deliberately ignored: this duplicates pegged
		// cache-capacity telemetry and is not an RPC lifecycle stage.
	default:
		// Unknown valid tags become one bounded other tuple. Payload values are
		// intentionally not decoded, retained, or exposed.
	}
	return record, nil
}

func parseConsensusRPCRequest(raw json.RawMessage) ([32]byte, string, bool, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return [32]byte{}, "", false, fmt.Errorf("invalid request object")
	}
	var queryPeers bool
	queryRaw, ok := body["query_peers"]
	if !ok || unmarshalRequiredJSON(queryRaw, &queryPeers) != nil {
		return [32]byte{}, "", false, fmt.Errorf("invalid query_peers")
	}
	contentRaw, ok := body["content"]
	if !ok {
		return [32]byte{}, "", false, fmt.Errorf("missing request content")
	}
	content := "other"
	var contentObject map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &contentObject); err != nil || contentObject == nil {
		return [32]byte{}, "", false, fmt.Errorf("invalid request content")
	}
	if _, ok := contentObject["BlocksAndTxs"]; ok {
		content = "blocks_and_txs"
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return [32]byte{}, "", false, err
	}
	return sha256.Sum256(canonical), content, queryPeers, nil
}

func parseConsensusRPCResponse(raw json.RawMessage) ([32]byte, string, string, int64, error) {
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return [32]byte{}, "", "", 0, err
	}
	digest := sha256.Sum256(canonical)
	var variants map[string]json.RawMessage
	if err := json.Unmarshal(raw, &variants); err != nil || variants == nil {
		return [32]byte{}, "", "", 0, fmt.Errorf("invalid response object")
	}
	okRaw, ok := variants["Ok"]
	if !ok {
		return digest, "other", "other", 0, nil
	}
	var okBody map[string]json.RawMessage
	if err := json.Unmarshal(okRaw, &okBody); err != nil || okBody == nil {
		return [32]byte{}, "", "", 0, fmt.Errorf("invalid Ok response")
	}
	blocksRaw, ok := okBody["BlocksAndTxs"]
	if !ok {
		return digest, "other", "other", 0, nil
	}
	var blocksBody map[string]json.RawMessage
	if err := json.Unmarshal(blocksRaw, &blocksBody); err != nil || blocksBody == nil {
		return [32]byte{}, "", "", 0, fmt.Errorf("invalid BlocksAndTxs response")
	}
	var n float64
	if rawN, present := blocksBody["n"]; !present || json.Unmarshal(rawN, &n) != nil || math.Trunc(n) != n || n < 2 || n > 100 {
		return [32]byte{}, "", "", 0, fmt.Errorf("invalid served block count")
	}
	return digest, "ok", "blocks_and_txs", int64(n), nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (t *consensusRPCTracker) accept(record consensusRPCRecord) string {
	now := record.sourceTime
	t.expire(now)
	switch record.tag {
	case "Rpc task size stats":
		return "ignored_known"
	case "Incoming tcp stream":
		if _, exists := t.bySession[record.session]; exists || len(t.bySession) >= t.max {
			return "unjoinable"
		}
		t.bySession[record.session] = &consensusRPCLifecycle{session: record.session, updatedAt: now}
		return "ok"
	case "Received rpc request":
		life := t.bySession[record.session]
		if life == nil || life.received || record.queryPeers {
			return "unjoinable"
		}
		if existing, collision := t.byRequest[record.request]; collision && existing != record.session {
			return "unjoinable"
		}
		life.request, life.content, life.received, life.updatedAt = record.request, record.content, true, now
		t.byRequest[record.request] = record.session
		return "ok"
	case "Rpc task inbound":
		life := t.lifecycleForRequest(record.request)
		if life == nil || !life.received || life.taskInbound || record.queryPeers {
			return "unjoinable"
		}
		life.taskInbound, life.updatedAt = true, now
		return "ok"
	case "Rpc task response":
		life := t.lifecycleForRequest(record.request)
		if life == nil || !life.taskInbound || life.taskResponse || record.queryPeers || !record.hasResponse {
			return "unjoinable"
		}
		life.taskResponse, life.response, life.outcome, life.blocks, life.updatedAt = true, record.response, record.outcome, record.blocks, now
		return "ok"
	case "Outbound response":
		life := t.bySession[record.session]
		if life == nil || !life.received || !life.taskInbound || !life.taskResponse ||
			life.request != record.request || life.response != record.response || record.queryPeers {
			return "unjoinable"
		}
		completionKey := sha256.Sum256(append(record.session[:], record.response[:]...))
		if _, duplicate := t.completed[completionKey]; duplicate {
			return "unjoinable"
		}
		if len(t.completed) >= t.max {
			var oldestKey [32]byte
			var oldestAt time.Time
			for key, completedAt := range t.completed {
				if oldestAt.IsZero() || completedAt.Before(oldestAt) {
					oldestKey, oldestAt = key, completedAt
				}
			}
			delete(t.completed, oldestKey)
		}
		t.completed[completionKey] = now
		// Emit stage counters only after the complete five-record lifecycle
		// joins. An abandoned or replayed prefix must not look like served work.
		metrics.HLConsensusRPCEvents.WithLabelValues("serve", "stream_opened", "observed", "other").Inc()
		metrics.HLConsensusRPCEvents.WithLabelValues("serve", "request_received", "observed", life.content).Inc()
		metrics.HLConsensusRPCEvents.WithLabelValues("serve", "task_inbound", "observed", life.content).Inc()
		metrics.HLConsensusRPCEvents.WithLabelValues("serve", "task_response", "observed", life.content).Inc()
		metrics.HLConsensusRPCEvents.WithLabelValues("serve", "response_sent", record.outcome, record.content).Inc()
		if record.outcome == "ok" && record.content == "blocks_and_txs" {
			metrics.HLConsensusRPCBlocksServed.Add(float64(record.blocks))
		}
		delete(t.byRequest, life.request)
		delete(t.bySession, life.session)
		return "ok"
	default:
		metrics.HLConsensusRPCEvents.WithLabelValues("other", "other", "other", "other").Inc()
		return "unknown_tag"
	}
}

func (t *consensusRPCTracker) lifecycleForRequest(request [32]byte) *consensusRPCLifecycle {
	session, ok := t.byRequest[request]
	if !ok {
		return nil
	}
	return t.bySession[session]
}

func (t *consensusRPCTracker) expire(now time.Time) {
	cutoff := now.Add(-t.ttl)
	for session, life := range t.bySession {
		if life.updatedAt.Before(cutoff) {
			delete(t.byRequest, life.request)
			delete(t.bySession, session)
		}
	}
	for key, completedAt := range t.completed {
		if completedAt.Before(cutoff) {
			delete(t.completed, key)
		}
	}
}
