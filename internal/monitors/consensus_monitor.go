package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// stores QC participation data for sliding window calc
type qcWindowEntry struct {
	timestamp time.Time
	signers   []string
}

// heartbeatInfo stores information about sent heartbeats
type heartbeatInfo struct {
	timestamp time.Time
	matched   bool
}

type heartbeatKey struct {
	validator string
	randomID  uint64
	round     uint64
}

type heartbeatAckKey struct {
	heartbeatKey
	source string
}

var latestEligibleStatus struct {
	sync.RWMutex
	snapshot statusSnapshot
	valid    bool
}

var heartbeatPeerAckSeries = struct {
	sync.Mutex
	seen map[[3]string]struct{}
}{seen: make(map[[3]string]struct{}, validatorSummaryLimit)}

type boundedBlockDedupe struct {
	max   int
	seen  map[string]struct{}
	order []string
}

func newBoundedBlockDedupe(max int) *boundedBlockDedupe {
	return &boundedBlockDedupe{max: max, seen: make(map[string]struct{}, max)}
}

func (d *boundedBlockDedupe) seenOrAdd(key string) bool {
	if key == "" {
		return false
	}
	if _, exists := d.seen[key]; exists {
		return true
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > d.max {
		delete(d.seen, d.order[0])
		d.order = d.order[1:]
	}
	return false
}

// Bounds on consensus-monitor maps. These are validator-keyed so the
// practical population is at most the active validator set (~25), but
// stale signers can accumulate across validator-set changes. A 1h
// last-seen TTL is plenty for the QC participation logic, which only
// cares about the recent sliding window.
const (
	consensusValidatorTTL = time.Hour
)

// monitors consensus-related logs and metrics
type ConsensusMonitor struct {
	config       *config.Config
	mapsMu       sync.Mutex
	qcSignatures map[string]int64     // Track QC signatures per signer
	qcLastSeen   map[string]time.Time // last-seen timestamp per signer
	tcVotes      map[string]int64     // Track TC votes per signer
	tcLastSeen   map[string]time.Time
	// sliding window tracking for participation rates
	qcWindow       []qcWindowEntry
	windowSize     int
	windowDuration time.Duration

	// block round tracking
	lastBlockRound       int64
	latestConsensusRound int64
	latestExecutionRound int64
	blockDedupe          *boundedBlockDedupe

	// throttle for the participation-rate recalculation
	lastQCRecalc time.Time

	// Heartbeat tracking
	heartbeats      map[heartbeatKey]heartbeatInfo
	heartbeatAcks   map[heartbeatAckKey]time.Time
	heartbeatsMutex sync.RWMutex

	lastConsensusSourceTime time.Time
	lastStatusSourceTime    time.Time

	// verification tracking
	verificationStats VerificationStats
	statsMutex        sync.RWMutex
}

// tracks internal counters for verification
type VerificationStats struct {
	LinesProcessed   int64
	BlocksProcessed  int64
	QCsProcessed     int64
	TCsProcessed     int64
	ParseErrors      int64
	LastProcessedAt  time.Time
	LastBlockRound   int64
	LastFilePosition int64
}

// creates a new consensus monitor
func NewConsensusMonitor(cfg *config.Config) *ConsensusMonitor {
	return &ConsensusMonitor{
		config:         cfg,
		qcSignatures:   make(map[string]int64),
		qcLastSeen:     make(map[string]time.Time),
		tcVotes:        make(map[string]int64),
		tcLastSeen:     make(map[string]time.Time),
		qcWindow:       make([]qcWindowEntry, 0),
		windowSize:     100,       // keep last 100 blocks for participation calculation
		windowDuration: time.Hour, // or calculate based on last hour
		heartbeats:     make(map[heartbeatKey]heartbeatInfo),
		heartbeatAcks:  make(map[heartbeatAckKey]time.Time),
		blockDedupe:    newBoundedBlockDedupe(4096),
	}
}

// trimStaleValidators drops qcSignatures / tcVotes entries that haven't been
// seen in consensusValidatorTTL. Called periodically by the monitor's
// housekeeping loop so the maps don't grow without bound as the validator
// set churns over time.
func (m *ConsensusMonitor) trimStaleValidators() {
	cutoff := time.Now().Add(-consensusValidatorTTL)
	m.mapsMu.Lock()
	defer m.mapsMu.Unlock()
	for v, t := range m.qcLastSeen {
		if t.Before(cutoff) {
			delete(m.qcLastSeen, v)
			delete(m.qcSignatures, v)
		}
	}
	for v, t := range m.tcLastSeen {
		if t.Before(cutoff) {
			delete(m.tcLastSeen, v)
			delete(m.tcVotes, v)
		}
	}

	// vote round/age series get a longer leash: the age is the stall
	// signal and should stay visible for a while before the series goes
	metrics.TrimVoteSeries(24 * time.Hour)
}

// core message types for consensus - using dedicated structures to match log format

// ConsensusVoteMessage matches the inner Vote payload in consensus logs.
//
// Wire formats:
//
//	outgoing: ["out", {"Vote": {"vote": {"validator": "0x...", "round": ..., "block_hash": "..."}, "destination": "0x..."}}]
//	incoming: ["in",  {"source": "0x...", "msg": {"Vote": {"validator": "0x...", "round": ..., "block_hash": "..."}}}]
//
// Old hl-node releases (pre-2026) used a `signer_id` field at this level; we
// keep that field as a fallback so the parser works against both schemas.
type ConsensusVoteMessage struct {
	Round     uint64 `json:"round"`
	Validator string `json:"validator"`
	SignerId  string `json:"signer_id"`
}

type ConsensusBlockMessage struct {
	// block info
	Round    uint64 `json:"round"`
	Proposer string `json:"proposer"`
	Hash     string `json:"hash"`
	// Evidence
	QC       *QCEvidence       `json:"qc,omitempty"`
	TC       json.RawMessage   `json:"tc,omitempty"`
	Payloads []json.RawMessage `json:"payloads,omitempty"`
}

type QCEvidence struct {
	Round     uint64   `json:"round"`
	BlockHash string   `json:"block_hash"`
	Signers   []string `json:"signers"`
}

// UnmarshalJSON keeps the certificate all-or-nothing. encoding/json accepts
// null for primitive destinations, which would otherwise turn a malformed QC
// into a plausible round zero / empty signer-set observation.
func (q *QCEvidence) UnmarshalJSON(data []byte) error {
	var wire struct {
		Round     json.RawMessage `json:"round"`
		BlockHash json.RawMessage `json:"block_hash"`
		Signers   json.RawMessage `json:"signers"`
	}
	if err := unmarshalRequiredJSON(data, &wire); err != nil {
		return fmt.Errorf("invalid QC object: %w", err)
	}

	var decoded QCEvidence
	if err := unmarshalRequiredJSON(wire.Round, &decoded.Round); err != nil || decoded.Round == 0 {
		return fmt.Errorf("invalid QC round")
	}
	if err := unmarshalRequiredJSON(wire.BlockHash, &decoded.BlockHash); err != nil || strings.TrimSpace(decoded.BlockHash) == "" {
		return fmt.Errorf("invalid QC block hash")
	}

	var rawSigners []json.RawMessage
	if err := unmarshalRequiredJSON(wire.Signers, &rawSigners); err != nil {
		return fmt.Errorf("invalid QC signers: %w", err)
	}
	decoded.Signers = make([]string, len(rawSigners))
	for i, rawSigner := range rawSigners {
		if err := unmarshalRequiredJSON(rawSigner, &decoded.Signers[i]); err != nil || strings.TrimSpace(decoded.Signers[i]) == "" {
			return fmt.Errorf("invalid QC signer %d", i)
		}
	}

	*q = decoded
	return nil
}

// helper function to format validator address for metrics
func (m *ConsensusMonitor) formatValidatorAddress(address string) string {
	if len(address) <= 10 {
		return address
	}
	// convert to lowercase and format as 0xABCD..EFGH
	addr := strings.ToLower(address)
	if !strings.HasPrefix(addr, "0x") {
		addr = "0x" + addr
	}
	if len(addr) > 10 {
		return fmt.Sprintf("%s..%s", addr[:6], addr[len(addr)-4:])
	}
	return addr
}

// returns current verification stats
func (m *ConsensusMonitor) GetVerificationStats() VerificationStats {
	m.statsMutex.RLock()
	defer m.statsMutex.RUnlock()
	return m.verificationStats
}

// starts monitoring consensus logs
func StartConsensusMonitor(ctx context.Context, cfg *config.Config, errCh chan<- error) {
	m := NewConsensusMonitor(cfg)
	for _, source := range []metrics.SourceID{
		metrics.SourceConsensus,
		metrics.SourceConsensusStatus,
		metrics.SourceConsensusRoundAdvance,
		metrics.SourceConsensusLocalStatus,
		metrics.SourceConsensusExecution,
		metrics.SourceConsensusRPC,
		metrics.SourceValidatorConnections,
	} {
		metrics.RegisterSource(source, true)
	}

	// load initial validator mappings
	if err := m.loadValidatorMappings(); err != nil {
		logger.WarningComponent("consensus", "Failed to load initial validator mappings: %v", err)
	}

	// start monitoring consensus logs
	goSafe("consensus", func() { m.monitorConsensusLogs(ctx, errCh) })

	// start monitoring status logs
	goSafe("consensus", func() { m.monitorStatusLogs(ctx, errCh) })
	goSafe("consensus", func() { monitorConsensusRPCLogs(ctx, cfg, errCh) })
	goSafe("consensus", func() { monitorValidatorConnectionLogs(ctx, cfg, errCh) })

	// Periodic housekeeping trims validator maps and also re-evaluates the
	// status/API join so a quiet source cannot leave rows beyond API freshness.
	goSafe("consensus", func() {
		trimTicker := time.NewTicker(10 * time.Minute)
		joinTicker := time.NewTicker(30 * time.Second)
		defer trimTicker.Stop()
		defer joinTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-trimTicker.C:
				m.trimStaleValidators()
			case now := <-joinTicker.C:
				refreshEligibleStatusSummariesAt(now)
			}
		}
	})
}

// monitors the consensus log files
func (m *ConsensusMonitor) monitorConsensusLogs(ctx context.Context, errCh chan<- error) {
	consensusDir := filepath.Join(m.config.NodeHome, "data", "node_logs", "consensus", "hourly")

	logger.InfoComponent("consensus", "Starting comprehensive consensus monitoring in: %s", consensusDir)

	var okLines, errLines int64
	tailStream(ctx, tailStreamOpts{
		component: "consensus",
		name:      "consensus stream",
		resolve: func() (string, error) {
			metrics.MarkSourceAttempt(metrics.SourceConsensus)
			if _, err := os.Stat(consensusDir); err != nil {
				if os.IsNotExist(err) {
					metrics.MarkSourceAbsent(metrics.SourceConsensus)
				} else {
					metrics.MarkSourceError(metrics.SourceConsensus, metrics.SourceFailureStat)
				}
				return "", err
			}
			path, err := m.getLatestConsensusLogFile()
			if err != nil {
				metrics.MarkSourceError(metrics.SourceConsensus, metrics.SourceFailureDiscovery)
				return "", err
			}
			return path, nil
		},
		rescanEvery: 2 * time.Second,
		eofSleep:    50 * time.Millisecond,
		bufSize:     1 << 20,
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("consensus")
			if err := m.processConsensusLine(line); err != nil {
				metrics.MarkSourceError(metrics.SourceConsensus, metrics.SourceFailureSchema)
				logger.DebugComponent("consensus", "Error processing consensus line: %v", err)
				errLines++
			} else {
				metrics.MarkSourceValidObservation(metrics.SourceConsensus, m.lastConsensusSourceTime)
				okLines++
			}
		},
		onSwitch: func(string) { metrics.MarkSourceAvailable(metrics.SourceConsensus) },
		// the per-line metric/stat updates used to take the global metrics
		// mutex several times per line at full consensus volume; flush once
		// per EOF pause instead
		onIdle: func() {
			if errLines > 0 {
				metrics.AddConsensusMonitorErrors("consensus", errLines)
				m.statsMutex.Lock()
				m.verificationStats.ParseErrors += errLines
				m.statsMutex.Unlock()
				errLines = 0
			}
			if okLines > 0 {
				metrics.AddConsensusMonitorLines("consensus", okLines)
				metrics.SetConsensusMonitorLastProcessed("consensus", time.Now().Unix())
				m.statsMutex.Lock()
				m.verificationStats.LinesProcessed += okLines
				m.verificationStats.LastProcessedAt = time.Now()
				m.statsMutex.Unlock()
				metrics.MarkSourcePublication(metrics.SourceConsensus)
				metrics.MarkMonitorValidObservation("consensus")
				metrics.MarkMonitorPublication("consensus")
				okLines = 0
			}
		},
	})
}

// processes a single line from consensus logs
func (m *ConsensusMonitor) processConsensusLine(line string) error {
	line = strings.TrimSpace(line)
	if len(line) == 0 || !strings.HasPrefix(line, "[") {
		return fmt.Errorf("invalid line format")
	}

	// parse the outer array struct [timestamp, [direction, message]]
	// use a minimal struct to avoid interface{} allocations
	var rawParts []json.RawMessage
	if err := json.Unmarshal([]byte(line), &rawParts); err != nil {
		return fmt.Errorf("json unmarshal: %w", err)
	}

	if len(rawParts) != 2 {
		return fmt.Errorf("unexpected log format")
	}

	// extract timestamp
	var timestampStr string
	if err := json.Unmarshal(rawParts[0], &timestampStr); err != nil {
		return fmt.Errorf("unmarshal timestamp: %w", err)
	}

	parsedTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestampStr)
	if err != nil {
		return fmt.Errorf("parse timestamp: %w", err)
	}
	m.lastConsensusSourceTime = parsedTime

	// parse the inner array [direction, message]
	var innerParts []json.RawMessage
	if err := json.Unmarshal(rawParts[1], &innerParts); err != nil {
		return fmt.Errorf("unmarshal inner parts: %w", err)
	}

	if len(innerParts) != 2 {
		return fmt.Errorf("invalid message format")
	}

	// extract direction
	var direction string
	if err := unmarshalRequiredJSON(innerParts[0], &direction); err != nil {
		return fmt.Errorf("unmarshal direction/tag: %w", err)
	}
	if strings.TrimSpace(direction) == "" {
		return fmt.Errorf("empty direction/tag")
	}

	// check message type using a minimal parse approach
	msgData := innerParts[1]
	switch direction {
	case "round advance":
		return m.processRoundAdvance(msgData, parsedTime)
	case "status":
		return m.processLocalConsensusStatus(msgData, parsedTime)
	case "execution state":
		return m.processExecutionState(msgData, parsedTime)
	}

	// first check if this has a nested msg struct (for "in" messages)
	var wrapper struct {
		Source string          `json:"source"`
		Msg    json.RawMessage `json:"msg"`
	}

	// try to unmarshal as wrapper first
	if err := json.Unmarshal(msgData, &wrapper); err == nil && len(wrapper.Msg) > 0 {
		// use the inner msg for processing
		msgData = wrapper.Msg
	}

	// try to detect message type by looking for specific fields
	if bytes.Contains(msgData, []byte(`"Vote"`)) {
		var msg struct {
			Vote json.RawMessage `json:"Vote"`
		}
		if err := json.Unmarshal(msgData, &msg); err == nil && len(msg.Vote) > 0 {
			var voteMsg ConsensusVoteMessage
			if err := json.Unmarshal(msg.Vote, &voteMsg); err == nil {
				// Outgoing votes wrap the payload one level deeper under
				// `vote`. If we got nothing useful at the top level, peel
				// one more layer and re-decode.
				if voteMsg.Validator == "" && voteMsg.SignerId == "" {
					var outer struct {
						Vote json.RawMessage `json:"vote"`
					}
					if json.Unmarshal(msg.Vote, &outer) == nil && len(outer.Vote) > 0 {
						_ = json.Unmarshal(outer.Vote, &voteMsg)
					}
				}
				// For incoming votes the validator who SENT the vote is the
				// outer "source"; fall back to that if the payload itself
				// didn't name them.
				if voteMsg.Validator == "" && voteMsg.SignerId == "" && wrapper.Source != "" {
					voteMsg.Validator = wrapper.Source
				}
				return m.processVoteStruct(&voteMsg, parsedTime)
			}
		}
		return fmt.Errorf("invalid Vote message")
	} else if bytes.Contains(msgData, []byte(`"Block"`)) {
		var msg struct {
			Block json.RawMessage `json:"Block"`
		}
		if err := json.Unmarshal(msgData, &msg); err == nil && len(msg.Block) > 0 {
			wireDirection := direction
			if wireDirection != "in" && wireDirection != "out" {
				wireDirection = "other"
			}
			return m.processBlockRaw(msg.Block, wireDirection)
		}
		return fmt.Errorf("invalid Block message")
	} else if bytes.Contains(msgData, []byte(`"Heartbeat"`)) && direction == "out" {
		var msg struct {
			Heartbeat json.RawMessage `json:"Heartbeat"`
		}
		if err := json.Unmarshal(msgData, &msg); err == nil && len(msg.Heartbeat) > 0 {
			var hbMsg HeartbeatMessage
			if err := json.Unmarshal(msg.Heartbeat, &hbMsg); err == nil {
				return m.processHeartbeatOut(&hbMsg, parsedTime)
			}
		}
		return fmt.Errorf("invalid Heartbeat message")
	} else if bytes.Contains(msgData, []byte(`"HeartbeatAck"`)) && direction == "in" {
		// Handle HeartbeatAck which might be in wrapper structure
		var msg struct {
			HeartbeatAck json.RawMessage `json:"HeartbeatAck"`
		}
		if err := json.Unmarshal(msgData, &msg); err == nil && len(msg.HeartbeatAck) > 0 {
			var ackMsg HeartbeatAckMessage
			if err := json.Unmarshal(msg.HeartbeatAck, &ackMsg); err == nil {
				return m.processHeartbeatAck(&ackMsg, wrapper.Source, parsedTime)
			}
		}
		return fmt.Errorf("invalid HeartbeatAck message")
	}

	return nil
}

// processes a vote message
func (m *ConsensusMonitor) processVoteStruct(vote *ConsensusVoteMessage, timestamp time.Time) error {
	// hl-node 2026+ exposes `validator` directly. Old releases used a
	// `signer_id` that we'd then look up. Try the explicit field first;
	// fall back to the signer mapping for backward compatibility.
	identifier := strings.TrimSpace(vote.Validator)
	if identifier == "" {
		identifier = strings.TrimSpace(vote.SignerId)
	}
	if identifier == "" || vote.Round == 0 {
		return fmt.Errorf("vote is missing validator identity or round")
	}

	// Preserve the raw wire identity for the registry join. Formatting here
	// would turn a resolvable signer into an unresolvable display string.
	metrics.SetValidatorLastVoteRound(identifier, int64(vote.Round))
	metrics.SetValidatorLastVoteTime(identifier, timestamp)
	metrics.HLConsensusAcceptedVoteObservations.Inc()

	return nil
}

func (m *ConsensusMonitor) processRoundAdvance(raw json.RawMessage, sourceTime time.Time) error {
	var event struct {
		PrevRound *int64  `json:"prev_round"`
		Round     *int64  `json:"round"`
		Reason    *string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		metrics.MarkSourceError(metrics.SourceConsensusRoundAdvance, metrics.SourceFailureDecode)
		return fmt.Errorf("unmarshal round advance: %w", err)
	}
	if event.PrevRound == nil || event.Round == nil || event.Reason == nil || strings.TrimSpace(*event.Reason) == "" {
		metrics.MarkSourceError(metrics.SourceConsensusRoundAdvance, metrics.SourceFailureSchema)
		return fmt.Errorf("round advance is missing required fields")
	}
	if *event.PrevRound < 0 || *event.Round <= 0 || *event.Round < *event.PrevRound {
		metrics.MarkSourceError(metrics.SourceConsensusRoundAdvance, metrics.SourceFailureSchema)
		return fmt.Errorf("invalid round advance values")
	}
	reason := "other"
	switch strings.ToLower(*event.Reason) {
	case "qc":
		reason = "qc"
	case "tc":
		reason = "tc"
	}
	metrics.HLConsensusRoundAdvanceEvents.WithLabelValues(reason).Inc()
	metrics.HLConsensusLocalRound.WithLabelValues("round_advance").Set(float64(*event.Round))
	m.publishConsensusRound(*event.Round)
	metrics.MarkSourceValidObservation(metrics.SourceConsensusRoundAdvance, sourceTime)
	metrics.MarkSourcePublication(metrics.SourceConsensusRoundAdvance)
	return nil
}

func (m *ConsensusMonitor) processLocalConsensusStatus(raw json.RawMessage, sourceTime time.Time) error {
	var event struct {
		Round           *int64 `json:"round"`
		LastVoteRound   *int64 `json:"last_vote_round"`
		LastCommitRound *int64 `json:"last_commit_round"`
		QCRound         *int64 `json:"qc_round"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		metrics.MarkSourceError(metrics.SourceConsensusLocalStatus, metrics.SourceFailureDecode)
		return fmt.Errorf("unmarshal local consensus status: %w", err)
	}
	if event.Round == nil || event.LastVoteRound == nil || event.LastCommitRound == nil || event.QCRound == nil {
		metrics.MarkSourceError(metrics.SourceConsensusLocalStatus, metrics.SourceFailureSchema)
		return fmt.Errorf("local consensus status is missing required fields")
	}
	if *event.Round <= 0 || *event.LastVoteRound < 0 || *event.LastCommitRound < 0 || *event.QCRound < 0 ||
		*event.LastVoteRound > *event.Round || *event.LastCommitRound > *event.Round || *event.QCRound > *event.Round {
		metrics.MarkSourceError(metrics.SourceConsensusLocalStatus, metrics.SourceFailureSchema)
		return fmt.Errorf("invalid local consensus status values")
	}
	for field, value := range map[string]int64{
		"status": *event.Round, "last_vote": *event.LastVoteRound,
		"last_commit": *event.LastCommitRound, "qc": *event.QCRound,
	} {
		metrics.HLConsensusLocalRound.WithLabelValues(field).Set(float64(value))
	}
	metrics.HLConsensusLocalRoundLag.WithLabelValues("last_vote").Set(float64(*event.Round - *event.LastVoteRound))
	metrics.HLConsensusLocalRoundLag.WithLabelValues("last_commit").Set(float64(*event.Round - *event.LastCommitRound))
	metrics.HLConsensusLocalRoundLag.WithLabelValues("qc").Set(float64(*event.Round - *event.QCRound))
	m.publishConsensusRound(*event.Round)
	metrics.MarkSourceValidObservation(metrics.SourceConsensusLocalStatus, sourceTime)
	metrics.MarkSourcePublication(metrics.SourceConsensusLocalStatus)
	return nil
}

func (m *ConsensusMonitor) processExecutionState(raw json.RawMessage, sourceTime time.Time) error {
	var event struct {
		Round int64 `json:"round"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		metrics.MarkSourceError(metrics.SourceConsensusExecution, metrics.SourceFailureDecode)
		return fmt.Errorf("unmarshal execution state: %w", err)
	}
	if event.Round <= 0 {
		metrics.MarkSourceError(metrics.SourceConsensusExecution, metrics.SourceFailureSchema)
		return fmt.Errorf("invalid execution-state round")
	}
	m.latestExecutionRound = event.Round
	metrics.HLConsensusLocalRound.WithLabelValues("execution").Set(float64(event.Round))
	if m.latestConsensusRound >= event.Round {
		metrics.HLConsensusLocalRoundLag.WithLabelValues("execution").Set(float64(m.latestConsensusRound - event.Round))
	} else {
		metrics.HLConsensusLocalRoundLag.DeleteLabelValues("execution")
	}
	metrics.MarkSourceValidObservation(metrics.SourceConsensusExecution, sourceTime)
	metrics.MarkSourcePublication(metrics.SourceConsensusExecution)
	return nil
}

func (m *ConsensusMonitor) publishConsensusRound(round int64) {
	if round <= 0 {
		return
	}
	m.latestConsensusRound = round
	metrics.SetCurrentConsensusRound(round)
	if m.latestExecutionRound > 0 && round >= m.latestExecutionRound {
		metrics.HLConsensusLocalRoundLag.WithLabelValues("execution").Set(float64(round - m.latestExecutionRound))
	} else {
		metrics.HLConsensusLocalRoundLag.DeleteLabelValues("execution")
	}
}

// processes the block message
func (m *ConsensusMonitor) processBlockRaw(blockData json.RawMessage, direction string) error {
	if direction != "in" && direction != "out" {
		direction = "other"
	}
	var block ConsensusBlockMessage
	if err := json.Unmarshal(blockData, &block); err != nil {
		return fmt.Errorf("unmarshal block: %w", err)
	}
	if block.Round == 0 || strings.TrimSpace(block.Proposer) == "" {
		return fmt.Errorf("block is missing round or proposer")
	}

	var tc *TCData
	trimmedTC := bytes.TrimSpace(block.TC)
	if len(trimmedTC) > 0 && !bytes.Equal(trimmedTC, []byte("null")) {
		if !isJSONObject(trimmedTC) {
			return fmt.Errorf("non-null TC is not an object")
		}
		var decoded TCData
		if err := json.Unmarshal(trimmedTC, &decoded); err != nil {
			return fmt.Errorf("unmarshal non-null TC: %w", err)
		}
		tc = &decoded
	}

	metrics.HLConsensusBlockDirectionEvents.WithLabelValues(direction).Inc()
	if m.blockDedupe.seenOrAdd(canonicalConsensusBlockKey(block)) {
		return nil
	}

	// get val addr for proposer
	proposer := m.getValidatorForSigner(block.Proposer)
	if proposer == "" {
		logger.DebugComponent("consensus", "No validator mapping for proposer: %s", block.Proposer)
		proposer = block.Proposer
	}
	formattedProposer := m.formatValidatorAddress(proposer)

	// log block processing
	logger.DebugComponent("consensus", "Processing block - Round: %d, Proposer: %s, Has TC: %v, Has QC: %v",
		block.Round, formattedProposer, tc != nil, block.QC != nil)

	// update consensus round metrics
	blockRound := int64(block.Round)
	if blockRound > 0 {
		m.publishConsensusRound(blockRound)
		metrics.HLConsensusLocalRound.WithLabelValues("block").Set(float64(blockRound))

		// calculate rounds per block if we have a previous round
		if m.lastBlockRound > 0 && blockRound > m.lastBlockRound {
			roundDiff := float64(blockRound - m.lastBlockRound)
			metrics.SetRoundsPerBlock(roundDiff)
		}
		m.lastBlockRound = blockRound

		// update verification stats
		m.statsMutex.Lock()
		m.verificationStats.BlocksProcessed++
		m.verificationStats.LastBlockRound = blockRound
		m.statsMutex.Unlock()
	}

	// update proposer metrics
	// note: proposer metrics are handled by replica monitor

	// process QC signatures
	if block.QC != nil {
		logger.DebugComponent("consensus", "Block has QC with %d signers", len(block.QC.Signers))
		if len(block.QC.Signers) > 0 {
			for _, signer := range block.QC.Signers {
				// use the signer ID directly as it's already in truncated format from logs
				validator := signer
				if validator == "" {
					logger.DebugComponent("consensus", "Empty signer in QC")
					continue
				}

				// track QC signatures (bounded by trimStaleValidators)
				m.mapsMu.Lock()
				m.qcSignatures[validator]++
				m.qcLastSeen[validator] = time.Now()
				m.mapsMu.Unlock()

				metrics.IncrementQCSignatures(validator)
			}

			logger.DebugComponent("consensus", "Processed QC with %d signers in block round %d",
				len(block.QC.Signers), block.Round)
		}

		// record QC size for histogram
		metrics.RecordQCSize(float64(len(block.QC.Signers)))

		// update verification stats
		m.statsMutex.Lock()
		m.verificationStats.QCsProcessed++
		m.statsMutex.Unlock()

		// add to sliding window for participation rate calculation
		m.addQCWindowEntry(block.QC.Signers)

		// recalculating participation re-emits every validator under the
		// global metrics mutex; at ~64ms block cadence doing that per block
		// is pure lock churn. A 2s gate keeps the panel fresh enough.
		if time.Since(m.lastQCRecalc) >= 2*time.Second {
			m.updateQCParticipationRates()
			m.lastQCRecalc = time.Now()
		}
	}

	if tc != nil {
		// record TC size
		metrics.RecordTCSize(float64(len(tc.Timeouts)))

		// update verification stats
		m.statsMutex.Lock()
		m.verificationStats.TCsProcessed++
		m.statsMutex.Unlock()

		// track each timeout voter
		for _, timeout := range tc.Timeouts {
			if timeout.Validator != "" {
				m.mapsMu.Lock()
				m.tcVotes[timeout.Validator]++
				m.tcLastSeen[timeout.Validator] = time.Now()
				m.mapsMu.Unlock()
				metrics.IncrementTCParticipation(timeout.Validator)
			}
		}

		logger.DebugComponent("consensus", "Processed TC with %d timeout votes in block round %d",
			len(tc.Timeouts), block.Round)

		// update TC metrics
		metrics.IncrementTCBlocks(block.Proposer)
	}

	// count block payloads if present
	// payload counting is handled by replica monitor

	// QC round lag is the nonnegative block.Round - block.QC.Round value
	// observed in the latest accepted Block carrying a complete QC.
	if block.QC != nil && block.QC.Round > 0 && block.Round > 0 {
		lag := int64(block.Round) - int64(block.QC.Round)
		if lag >= 0 {
			metrics.SetQCRoundLag(float64(lag))
		}
	}
	return nil
}

func canonicalConsensusBlockKey(block ConsensusBlockMessage) string {
	if block.Hash != "" {
		return "hash:" + block.Hash
	}
	if block.Round == 0 && block.Proposer == "" {
		return ""
	}
	qcRound := uint64(0)
	qcHash := ""
	if block.QC != nil {
		qcRound = block.QC.Round
		qcHash = block.QC.BlockHash
	}
	return fmt.Sprintf("round:%d|proposer:%s|qc:%d|qc_hash:%s", block.Round, block.Proposer, qcRound, qcHash)
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

// returns the validator address for a given signer
func (m *ConsensusMonitor) getValidatorForSigner(signer string) string {
	identity, ok := metrics.ResolveValidatorIdentity(signer)
	if !ok || identity.Kind != "signer" {
		return ""
	}
	return identity.Validator
}

// loads initial validator mappings from ABCI state
func (m *ConsensusMonitor) loadValidatorMappings() error {
	// for now, skip loading initial mappings - they will be populated from replica monitor
	// this avoids the ABCI state complexity for the QC counter fix
	return nil
}

// returns the path to the latest consensus log file
func (m *ConsensusMonitor) getLatestConsensusLogFile() (string, error) {
	consensusDir := filepath.Join(m.config.NodeHome, "data", "node_logs", "consensus", "hourly")
	return latestHourlyFile(consensusDir)
}

// addQCWindowEntry adds a new QC entry to the sliding window
func (m *ConsensusMonitor) addQCWindowEntry(signers []string) {
	entry := qcWindowEntry{
		timestamp: time.Now(),
		signers:   signers,
	}

	m.qcWindow = append(m.qcWindow, entry)

	// trim window by size
	if len(m.qcWindow) > m.windowSize {
		m.qcWindow = m.qcWindow[len(m.qcWindow)-m.windowSize:]
	}

	// also trim by time
	cutoff := time.Now().Add(-m.windowDuration)
	i := 0
	for i < len(m.qcWindow) && m.qcWindow[i].timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		m.qcWindow = m.qcWindow[i:]
	}
}

// processHeartbeatOut processes outgoing heartbeat messages
func (m *ConsensusMonitor) processHeartbeatOut(hb *HeartbeatMessage, timestamp time.Time) error {
	if hb.Validator == "" || hb.RandomID == 0 || hb.Round == 0 {
		return fmt.Errorf("missing heartbeat fields")
	}
	wireValidator, ok := normalizeWireAddress(hb.Validator)
	if !ok {
		return fmt.Errorf("invalid heartbeat validator")
	}
	key := heartbeatKey{validator: wireAddressKey(wireValidator), randomID: hb.RandomID, round: hb.Round}

	formattedValidator := m.formatValidatorAddress(hb.Validator)

	// Store heartbeat info
	m.heartbeatsMutex.Lock()
	m.heartbeats[key] = heartbeatInfo{timestamp: timestamp}

	// Clean up old heartbeats (older than 5 minutes)
	cutoff := timestamp.Add(-5 * time.Minute)
	for id, info := range m.heartbeats {
		if info.timestamp.Before(cutoff) {
			delete(m.heartbeats, id)
			if !info.matched {
				metrics.HLConsensusHeartbeatJoin.WithLabelValues("unknown", "expired").Inc()
			}
		}
	}
	for ackKey, seenAt := range m.heartbeatAcks {
		if seenAt.Before(cutoff) {
			delete(m.heartbeatAcks, ackKey)
		}
	}
	m.heartbeatsMutex.Unlock()

	// Increment heartbeat sent counter
	metrics.IncrementHeartbeatsSent(hb.Validator)

	logger.DebugComponent("consensus", "Registered outgoing heartbeat from %s", formattedValidator)

	return nil
}

// processHeartbeatAck processes heartbeat acknowledgment messages
func (m *ConsensusMonitor) processHeartbeatAck(ack *HeartbeatAckMessage, source string, timestamp time.Time) error {
	if ack.RandomID == 0 || ack.Round == 0 || ack.Validator == "" {
		return fmt.Errorf("missing heartbeat acknowledgement fields")
	}

	if source == "" {
		return fmt.Errorf("missing source in ack")
	}

	var ok bool
	source, ok = normalizeWireAddress(source)
	if !ok {
		return fmt.Errorf("invalid heartbeat acknowledgement source")
	}
	wireValidator, ok := normalizeWireAddress(ack.Validator)
	if !ok {
		return fmt.Errorf("invalid heartbeat acknowledgement validator")
	}
	sourceKey := wireAddressKey(source)
	key := heartbeatKey{validator: wireAddressKey(wireValidator), randomID: ack.RandomID, round: ack.Round}
	ackKey := heartbeatAckKey{heartbeatKey: key, source: sourceKey}

	// Look up the original heartbeat and reject a duplicate acknowledgement
	// without re-observing either latency distribution.
	m.heartbeatsMutex.Lock()
	hbInfo, exists := m.heartbeats[key]
	kind := "unknown"
	if exists {
		kind = "peer"
		if sourceKey == key.validator {
			kind = "self"
		}
	}
	if _, duplicate := m.heartbeatAcks[ackKey]; duplicate {
		m.heartbeatsMutex.Unlock()
		metrics.HLConsensusHeartbeatJoin.WithLabelValues(kind, "mismatch").Inc()
		return nil
	}
	m.heartbeatsMutex.Unlock()

	if !exists {
		outcome := "orphan"
		m.heartbeatsMutex.RLock()
		for candidate := range m.heartbeats {
			if candidate.randomID == ack.RandomID {
				outcome = "mismatch"
				break
			}
		}
		m.heartbeatsMutex.RUnlock()
		metrics.HLConsensusHeartbeatJoin.WithLabelValues("unknown", outcome).Inc()
		return nil
	}

	// calculate delay
	delay := timestamp.Sub(hbInfo.timestamp)
	if delay < 0 {
		metrics.HLConsensusHeartbeatJoin.WithLabelValues(kind, "mismatch").Inc()
		return nil
	}
	m.heartbeatsMutex.Lock()
	if _, duplicate := m.heartbeatAcks[ackKey]; duplicate {
		m.heartbeatsMutex.Unlock()
		metrics.HLConsensusHeartbeatJoin.WithLabelValues(kind, "mismatch").Inc()
		return nil
	}
	m.heartbeatAcks[ackKey] = timestamp
	hbInfo.matched = true
	m.heartbeats[key] = hbInfo
	m.heartbeatsMutex.Unlock()
	if sourceKey == key.validator {
		metrics.HLConsensusHeartbeatJoin.WithLabelValues("self", "matched").Inc()
		metrics.HLConsensusSelfHeartbeatLoopDuration.Observe(delay.Seconds())
		return nil
	}

	metrics.HLConsensusHeartbeatJoin.WithLabelValues("peer", "matched").Inc()
	metrics.HLConsensusHeartbeatPeerAckDelay.Observe(delay.Seconds())
	identity := metrics.ResolveSignerSnapshot([]string{source})[source]
	if identity.Validator == "unknown" {
		// This is a process-lifetime CounterVec. Unknown wire identities must
		// collapse to one bounded row instead of permanently admitting a new
		// signer label for every unrecognized source.
		identity = metrics.ValidatorIdentity{Validator: "unknown", Signer: "unknown", Name: "unknown", Kind: "signer"}
	}
	labels := [3]string{identity.Validator, identity.Signer, identity.Name}
	if admitHeartbeatPeerAckSeries(labels) {
		metrics.HLConsensusHeartbeatPeerAcks.WithLabelValues(labels[0], labels[1], labels[2]).Inc()
	}

	return nil
}

func admitHeartbeatPeerAckSeries(labels [3]string) bool {
	heartbeatPeerAckSeries.Lock()
	defer heartbeatPeerAckSeries.Unlock()
	if _, exists := heartbeatPeerAckSeries.seen[labels]; exists {
		return true
	}
	if len(heartbeatPeerAckSeries.seen) >= validatorSummaryLimit {
		return false
	}
	heartbeatPeerAckSeries.seen[labels] = struct{}{}
	return true
}

// calculates and updates QC participation rates for all validators
func (m *ConsensusMonitor) updateQCParticipationRates() {
	if len(m.qcWindow) == 0 {
		metrics.ReplaceQCParticipationRates(map[string]float64{})
		return
	}

	// count participation per validator
	participationCount := make(map[string]int)
	for _, entry := range m.qcWindow {
		for _, signer := range entry.signers {
			participationCount[signer]++
		}
	}

	// calculate rates
	totalBlocks := float64(len(m.qcWindow))

	rates := make(map[string]float64, len(participationCount))
	for validator, count := range participationCount {
		rates[validator] = (float64(count) / totalBlocks) * 100
	}
	metrics.ReplaceQCParticipationRates(rates)
}

// monitors the status log files for additional consensus info
func (m *ConsensusMonitor) monitorStatusLogs(ctx context.Context, errCh chan<- error) {
	statusDir := filepath.Join(m.config.NodeHome, "data", "node_logs", "status", "hourly")

	logger.InfoComponent("consensus", "Starting status log monitoring in: %s", statusDir)

	var okLines, errLines int64
	tailStream(ctx, tailStreamOpts{
		component: "consensus",
		name:      "status stream",
		resolve: func() (string, error) {
			metrics.MarkSourceAttempt(metrics.SourceConsensusStatus)
			if _, err := os.Stat(statusDir); err != nil {
				if os.IsNotExist(err) {
					metrics.MarkSourceAbsent(metrics.SourceConsensusStatus)
				} else {
					metrics.MarkSourceError(metrics.SourceConsensusStatus, metrics.SourceFailureStat)
				}
				return "", err
			}
			path, err := latestHourlyFile(statusDir)
			if err != nil {
				metrics.MarkSourceError(metrics.SourceConsensusStatus, metrics.SourceFailureDiscovery)
			}
			return path, err
		},
		rescanEvery: 5 * time.Second,
		eofSleep:    250 * time.Millisecond,
		bufSize:     1 << 20,
		onLine: func(line string) {
			if err := m.processStatusLine(line); err != nil {
				metrics.MarkSourceError(metrics.SourceConsensusStatus, metrics.SourceFailureSchema)
				logger.DebugComponent("consensus", "Error processing status line: %v", err)
				errLines++
			} else {
				okLines++
			}
		},
		onSwitch: func(string) { metrics.MarkSourceAvailable(metrics.SourceConsensusStatus) },
		onIdle: func() {
			metrics.WithPrometheusSnapshotUpdate(func() {
				if errLines > 0 {
					metrics.AddConsensusMonitorErrors("status", errLines)
					errLines = 0
				}
				if okLines > 0 {
					metrics.AddConsensusMonitorLines("status", okLines)
					metrics.SetConsensusMonitorLastProcessed("status", time.Now().Unix())
					metrics.MarkSourcePublication(metrics.SourceConsensusStatus)
					okLines = 0
				}
			})
		},
	})
}

// processStatusLine processes a single line from status logs
func (m *ConsensusMonitor) processStatusLine(line string) error {
	snapshot, err := parseStatusSnapshot([]byte(line))
	if err != nil {
		return err
	}

	now := time.Now()
	metrics.WithPrometheusSnapshotUpdate(func() {
		replaceLatestEligibleStatusSnapshot(snapshot)
		publishConsensusStatusDetailUnlocked(snapshot)
		eligible, apiUpdatedAt := metrics.GetAPIActiveAndUnjailedValidators()
		publishEligibleStatusSummariesUnlocked(snapshot, eligible, apiUpdatedAt, now)
		m.lastStatusSourceTime = snapshot.SourceTime
		metrics.MarkSourceValidObservation(metrics.SourceConsensusStatus, snapshot.SourceTime)
	})
	return nil
}

// publishConsensusStatusDetailUnlocked resolves API-enriched labels and
// replaces the entire status detail generation. Callers must hold the
// Prometheus snapshot barrier so registry replacement and detail publication
// cannot interleave.
func publishConsensusStatusDetailUnlocked(snapshot statusSnapshot) {
	signers := make([]string, 0, len(snapshot.Heartbeats)+2*len(snapshot.Disconnected))
	for _, hb := range snapshot.Heartbeats {
		signers = append(signers, hb.Signer)
	}
	for _, pair := range snapshot.Disconnected {
		signers = append(signers, pair.SubjectSigner, pair.ReporterSigner)
	}
	identities := metrics.ResolveSignerSnapshot(signers)

	heartbeats := make([]metrics.ValidatorHeartbeatSnapshot, 0, len(snapshot.Heartbeats))
	for _, hb := range snapshot.Heartbeats {
		identity := identities[hb.Signer]
		row := metrics.ValidatorHeartbeatSnapshot{Identity: identity}
		if hb.SinceLastSuccess.Present && !hb.SinceLastSuccess.Null {
			value := hb.SinceLastSuccess.Value
			row.SinceLastSuccess = &value
		}
		if hb.LastAckDuration.Present {
			row.AckFieldPresent = true
			if !hb.LastAckDuration.Null {
				value := hb.LastAckDuration.Value
				row.LastAckDuration = &value
			}
		}
		heartbeats = append(heartbeats, row)
	}

	disconnected := make([]metrics.ValidatorDisconnectSnapshot, 0, len(snapshot.Disconnected))
	for _, pair := range snapshot.Disconnected {
		subject := identities[pair.SubjectSigner]
		reporter := identities[pair.ReporterSigner]
		disconnected = append(disconnected, metrics.ValidatorDisconnectSnapshot{
			Subject: subject, Reporter: reporter, SinceRound: pair.SinceRound,
		})
	}

	metrics.ReplaceConsensusStatusSnapshot(heartbeats, disconnected)
	if snapshot.HeartbeatFieldPresent {
		metrics.HLConsensusStatusFieldReported.WithLabelValues("heartbeat_statuses").Set(1)
	} else {
		metrics.HLConsensusStatusFieldReported.WithLabelValues("heartbeat_statuses").Set(0)
	}
	if snapshot.DisconnectedPresent {
		metrics.HLConsensusStatusFieldReported.WithLabelValues("disconnected_validators").Set(1)
	} else {
		metrics.HLConsensusStatusFieldReported.WithLabelValues("disconnected_validators").Set(0)
	}
}

func parseStatusSnapshot(line []byte) (statusSnapshot, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return statusSnapshot{}, fmt.Errorf("unmarshal status envelope: %w", err)
	}
	if len(outer) != 2 {
		return statusSnapshot{}, fmt.Errorf("status envelope length %d", len(outer))
	}
	var timestamp string
	if err := json.Unmarshal(outer[0], &timestamp); err != nil {
		return statusSnapshot{}, fmt.Errorf("unmarshal status timestamp: %w", err)
	}
	sourceTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
	if err != nil {
		return statusSnapshot{}, fmt.Errorf("parse status timestamp: %w", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(outer[1], &body); err != nil {
		return statusSnapshot{}, fmt.Errorf("unmarshal status object: %w", err)
	}
	if body == nil {
		return statusSnapshot{}, fmt.Errorf("status body is not an object")
	}
	snapshot := statusSnapshot{SourceTime: sourceTime}
	rawRound, present := body["round"]
	if !present || json.Unmarshal(rawRound, &snapshot.Round) != nil || snapshot.Round <= 0 {
		return statusSnapshot{}, fmt.Errorf("invalid status round")
	}

	if raw, present := body["heartbeat_statuses"]; present {
		snapshot.HeartbeatFieldPresent = true
		rows, err := parseStatusHeartbeats(raw)
		if err != nil {
			return statusSnapshot{}, err
		}
		snapshot.Heartbeats = rows
	}
	if raw, present := body["disconnected_validators"]; present {
		snapshot.DisconnectedPresent = true
		pairs, err := parseStatusDisconnected(raw)
		if err != nil {
			return statusSnapshot{}, err
		}
		snapshot.Disconnected = pairs
	}
	if raw, present := body["validators_missing_heartbeat"]; present {
		snapshot.MissingHeartbeatPresent = true
		if !isJSONArray(raw) {
			return statusSnapshot{}, fmt.Errorf("validators_missing_heartbeat is not an array")
		}
		if err := json.Unmarshal(raw, &snapshot.MissingHeartbeatSigners); err != nil {
			return statusSnapshot{}, fmt.Errorf("invalid validators_missing_heartbeat: %w", err)
		}
		if len(snapshot.MissingHeartbeatSigners) > validatorSummaryLimit {
			return statusSnapshot{}, fmt.Errorf("validators_missing_heartbeat count %d exceeds limit %d", len(snapshot.MissingHeartbeatSigners), validatorSummaryLimit)
		}
		seen := make([]string, 0, len(snapshot.MissingHeartbeatSigners))
		for i, signer := range snapshot.MissingHeartbeatSigners {
			var ok bool
			signer, ok = normalizeWireAddress(signer)
			if !ok {
				return statusSnapshot{}, fmt.Errorf("invalid missing-heartbeat signer at %d", i)
			}
			var unique bool
			seen, unique = appendUniqueWireAddress(seen, signer)
			if !unique {
				return statusSnapshot{}, fmt.Errorf("duplicate missing-heartbeat signer")
			}
			snapshot.MissingHeartbeatSigners[i] = signer
		}
	}
	return snapshot, nil
}

func parseStatusHeartbeats(raw json.RawMessage) ([]statusHeartbeat, error) {
	if !isJSONArray(raw) {
		return nil, fmt.Errorf("heartbeat_statuses is not an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("invalid heartbeat_statuses: %w", err)
	}
	if len(entries) > validatorSummaryLimit {
		return nil, fmt.Errorf("heartbeat_statuses count %d exceeds limit %d", len(entries), validatorSummaryLimit)
	}
	out := make([]statusHeartbeat, 0, len(entries))
	seen := make([]string, 0, len(entries))
	for i, rawEntry := range entries {
		var entry []json.RawMessage
		if err := json.Unmarshal(rawEntry, &entry); err != nil || len(entry) != 2 {
			return nil, fmt.Errorf("invalid heartbeat row %d", i)
		}
		var signer string
		if err := json.Unmarshal(entry[0], &signer); err != nil {
			return nil, fmt.Errorf("invalid heartbeat signer %d", i)
		}
		var ok bool
		signer, ok = normalizeWireAddress(signer)
		if !ok {
			return nil, fmt.Errorf("invalid heartbeat signer %d", i)
		}
		seen, ok = appendUniqueWireAddress(seen, signer)
		if !ok {
			return nil, fmt.Errorf("duplicate heartbeat signer")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry[1], &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("invalid heartbeat body %d", i)
		}
		since, err := parseOptionalStatusFloat(fields, "since_last_success", false)
		if err != nil {
			return nil, fmt.Errorf("heartbeat row %d since_last_success: %w", i, err)
		}
		ack, err := parseOptionalStatusFloat(fields, "last_ack_duration", true)
		if err != nil {
			return nil, fmt.Errorf("heartbeat row %d last_ack_duration: %w", i, err)
		}
		out = append(out, statusHeartbeat{Signer: signer, SinceLastSuccess: since, LastAckDuration: ack})
	}
	return out, nil
}

func parseOptionalStatusFloat(fields map[string]json.RawMessage, key string, allowNull bool) (optionalStatusFloat, error) {
	raw, present := fields[key]
	if !present {
		return optionalStatusFloat{}, nil
	}
	value := optionalStatusFloat{Present: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if !allowNull {
			return optionalStatusFloat{}, fmt.Errorf("null is not valid for this field")
		}
		value.Null = true
		return value, nil
	}
	if err := json.Unmarshal(raw, &value.Value); err != nil || math.IsNaN(value.Value) || math.IsInf(value.Value, 0) || value.Value < 0 {
		return optionalStatusFloat{}, fmt.Errorf("expected nonnegative finite number or null")
	}
	return value, nil
}

func parseStatusDisconnected(raw json.RawMessage) ([]statusDisconnectedPair, error) {
	if !isJSONArray(raw) {
		return nil, fmt.Errorf("disconnected_validators is not an array")
	}
	var subjects []json.RawMessage
	if err := json.Unmarshal(raw, &subjects); err != nil {
		return nil, fmt.Errorf("invalid disconnected_validators: %w", err)
	}
	if len(subjects) > validatorSummaryLimit {
		return nil, fmt.Errorf("disconnected subject count %d exceeds limit %d", len(subjects), validatorSummaryLimit)
	}
	out := make([]statusDisconnectedPair, 0)
	seen := make([]wireAddressRelation, 0)
	seenSubjects := make([]string, 0, len(subjects))
	for i, rawSubject := range subjects {
		var subject []json.RawMessage
		if err := json.Unmarshal(rawSubject, &subject); err != nil || len(subject) != 2 {
			return nil, fmt.Errorf("invalid disconnected subject row %d", i)
		}
		var subjectSigner string
		if err := json.Unmarshal(subject[0], &subjectSigner); err != nil {
			return nil, fmt.Errorf("invalid disconnected subject signer %d", i)
		}
		var ok bool
		subjectSigner, ok = normalizeWireAddress(subjectSigner)
		if !ok {
			return nil, fmt.Errorf("invalid disconnected subject signer %d", i)
		}
		seenSubjects, ok = appendUniqueWireAddress(seenSubjects, subjectSigner)
		if !ok {
			return nil, fmt.Errorf("duplicate disconnected subject signer %d", i)
		}
		if !isJSONArray(subject[1]) {
			return nil, fmt.Errorf("invalid disconnected reporter list %d", i)
		}
		var reporters []json.RawMessage
		if err := json.Unmarshal(subject[1], &reporters); err != nil {
			return nil, fmt.Errorf("invalid disconnected reporter list %d", i)
		}
		if len(reporters) > validatorSummaryLimit || len(out) > validatorSummaryLimit-len(reporters) {
			return nil, fmt.Errorf("disconnected reporter count exceeds limit %d", validatorSummaryLimit)
		}
		for j, rawReporter := range reporters {
			var reporter []json.RawMessage
			if err := json.Unmarshal(rawReporter, &reporter); err != nil || len(reporter) != 2 {
				return nil, fmt.Errorf("invalid disconnected reporter row %d/%d", i, j)
			}
			var reporterSigner string
			var sinceRound int64
			if err := json.Unmarshal(reporter[0], &reporterSigner); err != nil {
				return nil, fmt.Errorf("invalid reporter signer %d/%d", i, j)
			}
			if err := unmarshalRequiredJSON(reporter[1], &sinceRound); err != nil || sinceRound < 0 {
				return nil, fmt.Errorf("invalid since_round %d/%d", i, j)
			}
			reporterSigner, ok = normalizeWireAddress(reporterSigner)
			if !ok {
				return nil, fmt.Errorf("invalid reporter signer %d/%d", i, j)
			}
			candidate := wireAddressRelation{subject: subjectSigner, reporter: reporterSigner}
			seen, ok = appendUniqueWireRelation(seen, candidate)
			if !ok {
				return nil, fmt.Errorf("duplicate disconnected reporter pair")
			}
			out = append(out, statusDisconnectedPair{SubjectSigner: subjectSigner, ReporterSigner: reporterSigner, SinceRound: sinceRound})
		}
	}
	return out, nil
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']'
}

func publishEligibleStatusSummariesAt(snapshot statusSnapshot, eligible []metrics.EligibleValidator, apiUpdatedAt, now time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		publishEligibleStatusSummariesUnlocked(snapshot, eligible, apiUpdatedAt, now)
	})
}

func replaceLatestEligibleStatusSnapshot(snapshot statusSnapshot) {
	copySnapshot := snapshot
	copySnapshot.Heartbeats = append([]statusHeartbeat(nil), snapshot.Heartbeats...)
	copySnapshot.Disconnected = append([]statusDisconnectedPair(nil), snapshot.Disconnected...)
	copySnapshot.MissingHeartbeatSigners = append([]string(nil), snapshot.MissingHeartbeatSigners...)
	latestEligibleStatus.Lock()
	latestEligibleStatus.snapshot = copySnapshot
	latestEligibleStatus.valid = true
	latestEligibleStatus.Unlock()
}

func clearLatestEligibleStatusSnapshot() {
	latestEligibleStatus.Lock()
	latestEligibleStatus.snapshot = statusSnapshot{}
	latestEligibleStatus.valid = false
	latestEligibleStatus.Unlock()
}

func currentEligibleStatusSnapshot() (statusSnapshot, bool) {
	latestEligibleStatus.RLock()
	snapshot := latestEligibleStatus.snapshot
	valid := latestEligibleStatus.valid
	latestEligibleStatus.RUnlock()
	return snapshot, valid
}

func refreshEligibleStatusSummariesAt(now time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		refreshEligibleStatusSummariesUnlocked(now)
	})
}

func refreshEligibleStatusSummariesUnlocked(now time.Time) {
	snapshot, ok := currentEligibleStatusSnapshot()
	if !ok {
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("missing_heartbeat")
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("disconnected")
		return
	}
	eligible, apiUpdatedAt := metrics.GetAPIActiveAndUnjailedValidators()
	publishEligibleStatusSummariesUnlocked(snapshot, eligible, apiUpdatedAt, now)
}

func refreshRetainedConsensusStatusUnlocked(now time.Time) {
	snapshot, ok := currentEligibleStatusSnapshot()
	if !ok {
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("missing_heartbeat")
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("disconnected")
		return
	}
	publishConsensusStatusDetailUnlocked(snapshot)
	eligible, apiUpdatedAt := metrics.GetAPIActiveAndUnjailedValidators()
	publishEligibleStatusSummariesUnlocked(snapshot, eligible, apiUpdatedAt, now)
}

func publishEligibleStatusSummariesUnlocked(snapshot statusSnapshot, eligible []metrics.EligibleValidator, apiUpdatedAt, now time.Time) {
	if apiUpdatedAt.IsZero() || now.Sub(apiUpdatedAt) > validatorAPITargetFreshness || apiUpdatedAt.After(now.Add(time.Minute)) {
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("missing_heartbeat")
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("disconnected")
		return
	}
	eligibleSigners := make(map[string]struct{}, len(eligible))
	for _, row := range eligible {
		eligibleSigners[strings.ToLower(row.Signer)] = struct{}{}
	}
	if snapshot.MissingHeartbeatPresent {
		count := 0
		for _, signer := range snapshot.MissingHeartbeatSigners {
			if _, ok := eligibleSigners[strings.ToLower(metrics.ExpandAddress(signer))]; ok {
				count++
			}
		}
		metrics.HLConsensusStatusEligibleSummary.WithLabelValues("missing_heartbeat").Set(float64(count))
	} else {
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("missing_heartbeat")
	}
	if snapshot.DisconnectedPresent {
		disconnectedEligible := make(map[string]struct{})
		for _, pair := range snapshot.Disconnected {
			signer := strings.ToLower(metrics.ExpandAddress(pair.SubjectSigner))
			if _, ok := eligibleSigners[signer]; ok {
				disconnectedEligible[signer] = struct{}{}
			}
		}
		metrics.HLConsensusStatusEligibleSummary.WithLabelValues("disconnected").Set(float64(len(disconnectedEligible)))
	} else {
		metrics.HLConsensusStatusEligibleSummary.DeleteLabelValues("disconnected")
	}
}
