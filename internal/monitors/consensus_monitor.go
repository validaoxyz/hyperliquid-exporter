package monitors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

// stores QC participation data for sliding window calc
type qcWindowEntry struct {
	timestamp time.Time
	signers   []string
}

// heartbeatInfo stores information about sent heartbeats
type heartbeatInfo struct {
	validator string
	timestamp time.Time
}

// monitors consensus-related logs and metrics
type ConsensusMonitor struct {
	config         *config.Config
	qcSignatures   map[string]int64  // Track QC signatures per signer
	tcVotes        map[string]int64  // Track TC votes per signer
	validatorCache map[string]string // Map signer address to validator address

	// sliding window tracking for participation rates
	qcWindow       []qcWindowEntry
	windowSize     int
	windowDuration time.Duration

	// block round tracking
	lastBlockRound int64

	// connectivity tracking
	disconnectedSet   map[string]bool
	disconnectedMutex sync.RWMutex

	// Heartbeat tracking
	heartbeats      map[float64]heartbeatInfo // randomID -> heartbeat info
	heartbeatsMutex sync.RWMutex

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
		config:          cfg,
		qcSignatures:    make(map[string]int64),
		tcVotes:         make(map[string]int64),
		validatorCache:  make(map[string]string),
		qcWindow:        make([]qcWindowEntry, 0),
		windowSize:      100,       // keep last 100 blocks for participation calculation
		windowDuration:  time.Hour, // or calculate based on last hour
		disconnectedSet: make(map[string]bool),
		heartbeats:      make(map[float64]heartbeatInfo),
	}
}

// core message types for consensus - using dedicated structures to match log format

type ConsensusVoteMessage struct {
	// only used for tracking TC votes
	Round    uint64 `json:"round"`
	SignerId string `json:"signer_id"`
}

type ConsensusBlockMessage struct {
	// block info
	Round    uint64 `json:"round"`
	Proposer string `json:"proposer"`
	// Evidence
	QC       *QCEvidence       `json:"qc,omitempty"`
	TC       json.RawMessage   `json:"tc,omitempty"`
	Payloads []json.RawMessage `json:"payloads,omitempty"`
}

type QCEvidence struct {
	Signers []string `json:"signers"`
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

	// load initial validator mappings
	if err := m.loadValidatorMappings(); err != nil {
		logger.WarningComponent("consensus", "Failed to load initial validator mappings: %v", err)
	}

	// start monitoring consensus logs
	go m.monitorConsensusLogs(ctx, errCh)

	// start monitoring status logs
	go m.monitorStatusLogs(ctx, errCh)
}

// monitors the consensus log files
func (m *ConsensusMonitor) monitorConsensusLogs(ctx context.Context, errCh chan<- error) {
	consensusDir := filepath.Join(m.config.NodeHome, "data", "node_logs", "consensus", "hourly")

	// check if consensus log directory exists
	if _, err := os.Stat(consensusDir); os.IsNotExist(err) {
		logger.InfoComponent("consensus", "Consensus logs not available (non-validator node) - skipping consensus monitoring")
		return
	}

	logger.InfoComponent("consensus", "Starting comprehensive consensus monitoring in: %s", consensusDir)

	var currentFile string
	var file *os.File
	var fileReader *bufio.Reader
	isFirstRun := true

	for {
		select {
		case <-ctx.Done():
			if file != nil {
				file.Close()
			}
			return
		default:
			// find the latest log file
			latestFile, err := m.getLatestConsensusLogFile()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					time.Sleep(10 * time.Second)
					continue
				}
				errCh <- fmt.Errorf("error finding latest consensus log file: %w", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if latestFile == "" {
				time.Sleep(10 * time.Second)
				continue
			}

			// switch to new file if needed
			if latestFile != currentFile {
				if file != nil {
					file.Close()
					file = nil
					fileReader = nil
				}

				file, err = os.Open(latestFile)
				if err != nil {
					errCh <- fmt.Errorf("error opening consensus log file: %w", err)
					time.Sleep(1 * time.Second)
					continue
				}

				// skip to end on first run
				if isFirstRun {
					_, err = file.Seek(0, io.SeekEnd)
					if err != nil {
						errCh <- fmt.Errorf("error seeking to end of file: %w", err)
						file.Close()
						file = nil
						time.Sleep(1 * time.Second)
						continue
					}
					logger.InfoComponent("consensus", "First run: starting to stream from the end of file %s", latestFile)
					isFirstRun = false
				} else {
					logger.InfoComponent("consensus", "Not first run: reading entire file %s", latestFile)
				}

				fileReader = bufio.NewReader(file)
				currentFile = latestFile
			}

			// read and process lines
			if fileReader != nil {
				for {
					line, err := fileReader.ReadString('\n')
					if err != nil {
						if err == io.EOF {
							time.Sleep(10 * time.Millisecond)
							break
						}
						errCh <- fmt.Errorf("error reading consensus log: %w", err)
						break
					}

					if err := m.processConsensusLine(line); err != nil {
						logger.DebugComponent("consensus", "Error processing consensus line: %v", err)
						metrics.IncrementConsensusMonitorErrors("consensus")
						m.statsMutex.Lock()
						m.verificationStats.ParseErrors++
						m.statsMutex.Unlock()
					} else {
						metrics.IncrementConsensusMonitorLines("consensus")
						metrics.SetConsensusMonitorLastProcessed("consensus", time.Now().Unix())
						m.statsMutex.Lock()
						m.verificationStats.LinesProcessed++
						m.verificationStats.LastProcessedAt = time.Now()
						m.statsMutex.Unlock()
					}
				}
			}
		}
	}
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

	if len(rawParts) < 2 {
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

	// parse the inner array [direction, message]
	var innerParts []json.RawMessage
	if err := json.Unmarshal(rawParts[1], &innerParts); err != nil {
		return fmt.Errorf("unmarshal inner parts: %w", err)
	}

	if len(innerParts) < 2 {
		return fmt.Errorf("invalid message format")
	}

	// extract direction
	var direction string
	if err := json.Unmarshal(innerParts[0], &direction); err != nil {
		direction = ""
	}

	// check message type using a minimal parse approach
	msgData := innerParts[1]

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
				return m.processVoteStruct(&voteMsg, parsedTime)
			}
		}
	} else if bytes.Contains(msgData, []byte(`"Block"`)) {
		var msg struct {
			Block json.RawMessage `json:"Block"`
		}
		if err := json.Unmarshal(msgData, &msg); err == nil && len(msg.Block) > 0 {
			return m.processBlockRaw(msg.Block)
		}
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
	}

	return nil
}

// processes a vote message
func (m *ConsensusMonitor) processVoteStruct(vote *ConsensusVoteMessage, timestamp time.Time) error {
	// get val addr for the signer
	validator := m.getValidatorForSigner(vote.SignerId)
	if validator == "" {
		return nil
	}

	formattedValidator := m.formatValidatorAddress(validator)

	// update vote round tracking
	if vote.Round > 0 {
		metrics.SetValidatorLastVoteRound(formattedValidator, int64(vote.Round))
	}

	// update vote time difference
	timeDiff := time.Since(timestamp).Seconds()
	metrics.SetValidatorVoteTimeDiff(formattedValidator, timeDiff)

	return nil
}

// processes the block message
func (m *ConsensusMonitor) processBlockRaw(blockData json.RawMessage) error {
	var block ConsensusBlockMessage
	if err := json.Unmarshal(blockData, &block); err != nil {
		return fmt.Errorf("unmarshal block: %w", err)
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
		block.Round, formattedProposer, block.TC != nil, block.QC != nil)

	// update consensus round metrics
	blockRound := int64(block.Round)
	if blockRound > 0 {
		metrics.SetCurrentConsensusRound(blockRound)

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

				formattedValidator := m.formatValidatorAddress(validator)

				// track QC signatures
				m.qcSignatures[validator]++

				metrics.IncrementQCSignatures(formattedValidator)
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

		// calculate and update participation rates
		m.updateQCParticipationRates()
	}

	if len(block.TC) > 0 {
		var tc TCData
		if err := json.Unmarshal(block.TC, &tc); err == nil {
			// record TC size
			metrics.RecordTCSize(float64(len(tc.Timeouts)))

			// update verification stats
			m.statsMutex.Lock()
			m.verificationStats.TCsProcessed++
			m.statsMutex.Unlock()

			// track each timeout voter
			for _, timeout := range tc.Timeouts {
				if timeout.Validator != "" {
					// timeout.Validator is already a validator address (truncated), not a signer
					// just format it directly
					formattedValidator := m.formatValidatorAddress(timeout.Validator)

					// track TC votes
					m.tcVotes[timeout.Validator]++
					metrics.IncrementTCParticipation(formattedValidator)
				}
			}

			logger.DebugComponent("consensus", "Processed TC with %d timeout votes in block round %d",
				len(tc.Timeouts), block.Round)
		} else {
			logger.DebugComponent("consensus", "Failed to parse TC data: %v", err)
		}

		// update TC metrics
		metrics.IncrementTCBlocks(formattedProposer)
	}

	// count block payloads if present
	// payload counting is handled by replica monitor

	// QC round lag would need QC round information which isn't in the current log format
	return nil
}

// returns the validator address for a given signer
func (m *ConsensusMonitor) getValidatorForSigner(signer string) string {
	// first check cache
	if validator, ok := m.validatorCache[signer]; ok {
		return validator
	}

	// try to get from metrics package (which maintains a global mapping)
	validator, exists := metrics.GetValidatorForSigner(signer)
	if exists && validator != "" {
		m.validatorCache[signer] = validator
		return validator
	}

	// if not found, the signer might be the validator itself
	// return empty to indicate no mapping found
	return ""
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
	return utils.GetLatestFile(consensusDir)
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
	if hb.Validator == "" || hb.RandomID == 0 {
		return fmt.Errorf("missing heartbeat fields")
	}

	// Get validator address
	validator := m.getValidatorForSigner(hb.Validator)
	if validator == "" {
		validator = hb.Validator
	}
	formattedValidator := m.formatValidatorAddress(validator)

	// Store heartbeat info
	m.heartbeatsMutex.Lock()
	m.heartbeats[hb.RandomID] = heartbeatInfo{
		validator: validator,
		timestamp: timestamp,
	}

	// Clean up old heartbeats (older than 5 minutes)
	cutoff := time.Now().Add(-5 * time.Minute)
	for id, info := range m.heartbeats {
		if info.timestamp.Before(cutoff) {
			delete(m.heartbeats, id)
		}
	}
	m.heartbeatsMutex.Unlock()

	// Increment heartbeat sent counter
	metrics.IncrementHeartbeatsSent(formattedValidator)

	logger.DebugComponent("consensus", "Registered outgoing heartbeat from %s with ID %.0f", formattedValidator, hb.RandomID)

	return nil
}

// processHeartbeatAck processes heartbeat acknowledgment messages
func (m *ConsensusMonitor) processHeartbeatAck(ack *HeartbeatAckMessage, source string, timestamp time.Time) error {
	if ack.RandomID == 0 {
		return fmt.Errorf("missing random_id in ack")
	}

	if source == "" {
		return fmt.Errorf("missing source in ack")
	}

	// Look up the original heartbeat
	m.heartbeatsMutex.RLock()
	hbInfo, exists := m.heartbeats[ack.RandomID]
	m.heartbeatsMutex.RUnlock()

	if !exists {
		// Heartbeat not found - might be from before we started monitoring
		return nil
	}

	// calculate delay
	delay := timestamp.Sub(hbInfo.timestamp)

	// get formatted addresses
	fromValidator := m.formatValidatorAddress(hbInfo.validator)
	toValidator := m.formatValidatorAddress(source)

	// update metrics
	metrics.IncrementHeartbeatAcksReceived(fromValidator, toValidator)
	metrics.RecordHeartbeatAckDelay(fromValidator, toValidator, float64(delay.Milliseconds()))

	logger.DebugComponent("consensus", "Heartbeat ack from %s to %s, delay: %v", toValidator, fromValidator, delay)

	return nil
}

// calculates and updates QC participation rates for all validators
func (m *ConsensusMonitor) updateQCParticipationRates() {
	if len(m.qcWindow) == 0 {
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

	// update metrics for validators who participated
	for validator, count := range participationCount {
		rate := (float64(count) / totalBlocks) * 100
		formattedValidator := m.formatValidatorAddress(validator)
		metrics.SetQCParticipationRate(formattedValidator, rate)
	}

	// set rate to 0 for validators who haven't participated
	for validator := range m.qcSignatures {
		if _, exists := participationCount[validator]; !exists {
			formattedValidator := m.formatValidatorAddress(validator)
			metrics.SetQCParticipationRate(formattedValidator, 0)
		}
	}
}

// monitors the status log files for additional consensus info
func (m *ConsensusMonitor) monitorStatusLogs(ctx context.Context, errCh chan<- error) {
	statusDir := filepath.Join(m.config.NodeHome, "data", "node_logs", "status", "hourly")

	// check if status log dir exists
	if _, err := os.Stat(statusDir); os.IsNotExist(err) {
		logger.InfoComponent("consensus", "Status logs not available (non-validator node) - skipping status monitoring")
		return
	}

	logger.InfoComponent("consensus", "Starting status log monitoring in: %s", statusDir)

	var currentFile string
	var fileReader *bufio.Reader
	isFirstRun := true

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// find latest log file
			latestFile, err := utils.GetLatestFile(statusDir)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					time.Sleep(10 * time.Second)
					continue
				}
				errCh <- fmt.Errorf("error finding latest status log file: %w", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if latestFile == "" {
				time.Sleep(10 * time.Second)
				continue
			}

			// switch to new file if needed
			if latestFile != currentFile {
				if fileReader != nil {
					fileReader = nil
				}

				file, err := os.Open(latestFile)
				if err != nil {
					errCh <- fmt.Errorf("error opening status log file: %w", err)
					time.Sleep(1 * time.Second)
					continue
				}

				// skip to end on first run
				if isFirstRun {
					_, err = file.Seek(0, io.SeekEnd)
					if err != nil {
						errCh <- fmt.Errorf("error seeking to end of file: %w", err)
						file.Close()
						time.Sleep(1 * time.Second)
						continue
					}
					logger.InfoComponent("consensus", "First run: starting to stream status from the end of file %s", latestFile)
					isFirstRun = false
				} else {
					logger.InfoComponent("consensus", "Not first run: reading entire status file %s", latestFile)
				}

				fileReader = bufio.NewReader(file)
				currentFile = latestFile
			}

			// read and process lines
			if fileReader != nil {
				for {
					line, err := fileReader.ReadString('\n')
					if err != nil {
						if err == io.EOF {
							time.Sleep(10 * time.Millisecond)
							break
						}
						errCh <- fmt.Errorf("error reading status log: %w", err)
						break
					}

					if err := m.processStatusLine(line); err != nil {
						logger.DebugComponent("consensus", "Error processing status line: %v", err)
						metrics.IncrementConsensusMonitorErrors("status")
					} else {
						metrics.IncrementConsensusMonitorLines("status")
						metrics.SetConsensusMonitorLastProcessed("status", time.Now().Unix())
					}
				}
			}
		}
	}
}

// processStatusLine processes a single line from status logs
func (m *ConsensusMonitor) processStatusLine(line string) error {
	// parse the outer array [timestamp, data]
	var rawParts []json.RawMessage
	if err := json.Unmarshal([]byte(line), &rawParts); err != nil {
		return fmt.Errorf("json unmarshal: %w", err)
	}

	if len(rawParts) < 2 {
		return fmt.Errorf("unexpected status log format")
	}

	// parse status data
	var statusData struct {
		DisconnectedValidators json.RawMessage `json:"disconnected_validators"`
		HeartbeatStatuses      json.RawMessage `json:"heartbeat_statuses"`
	}

	if err := json.Unmarshal(rawParts[1], &statusData); err != nil {
		return fmt.Errorf("unmarshal status data: %w", err)
	}

	// process disconnected validators
	if len(statusData.DisconnectedValidators) > 0 {
		m.processDisconnectedValidatorsRaw(statusData.DisconnectedValidators)
	}

	// process heartbeat statuses
	if len(statusData.HeartbeatStatuses) > 0 {
		m.processHeartbeatStatusesRaw(statusData.HeartbeatStatuses)
	}

	return nil
}

// processes disconnected validators from raw JSON
func (m *ConsensusMonitor) processDisconnectedValidatorsRaw(data json.RawMessage) {
	// parse as array of [validator, [[peer, round], ...]]
	var discList []json.RawMessage
	if err := json.Unmarshal(data, &discList); err != nil {
		logger.DebugComponent("consensus", "Failed to parse disconnected validators: %v", err)
		return
	}

	newDisconnected := make(map[string]bool)

	for _, item := range discList {
		var entry []json.RawMessage
		if err := json.Unmarshal(item, &entry); err != nil || len(entry) < 2 {
			continue
		}

		var valAddr string
		if err := json.Unmarshal(entry[0], &valAddr); err != nil {
			continue
		}

		var peerList [][]json.RawMessage
		if err := json.Unmarshal(entry[1], &peerList); err != nil {
			continue
		}

		for _, peerData := range peerList {
			if len(peerData) > 0 {
				var peerAddr string
				if err := json.Unmarshal(peerData[0], &peerAddr); err == nil {
					key := fmt.Sprintf("%s_%s", valAddr, peerAddr)
					newDisconnected[key] = true
				}
			}
		}
	}

	// update metrics, only track disconnected pairs to reduce cardinality
	m.disconnectedMutex.Lock()

	// remove metrics for validators that are now connected
	for oldKey := range m.disconnectedSet {
		if _, stillDisconnected := newDisconnected[oldKey]; !stillDisconnected {
			// validator is now connected - remove the metric entirely instead of setting to 1
			parts := strings.Split(oldKey, "_")
			if len(parts) == 2 {
				// this will remove the metric from the labeledValues map
				metrics.RemoveValidatorConnectivity(parts[0], parts[1])
			}
		}
	}

	// only set metrics for disconnected pairs (value 0)
	for newKey := range newDisconnected {
		if _, wasDisconnected := m.disconnectedSet[newKey]; !wasDisconnected {
			parts := strings.Split(newKey, "_")
			if len(parts) == 2 {
				metrics.SetValidatorConnectivity(parts[0], parts[1], 0)
			}
		}
	}

	m.disconnectedSet = newDisconnected
	m.disconnectedMutex.Unlock()
}

// processes heartbeat status information from raw JSON
func (m *ConsensusMonitor) processHeartbeatStatusesRaw(data json.RawMessage) {
	// parse as array of [validator, {status_info}]
	var hsList []json.RawMessage
	if err := json.Unmarshal(data, &hsList); err != nil {
		logger.DebugComponent("consensus", "Failed to parse heartbeat statuses: %v", err)
		return
	}

	for _, item := range hsList {
		var entry []json.RawMessage
		if err := json.Unmarshal(item, &entry); err != nil || len(entry) < 2 {
			continue
		}

		var valAddr string
		if err := json.Unmarshal(entry[0], &valAddr); err != nil {
			continue
		}

		var info struct {
			SinceLastSuccess float64 `json:"since_last_success"`
			LastAckDuration  float64 `json:"last_ack_duration"`
		}
		if err := json.Unmarshal(entry[1], &info); err != nil {
			continue
		}

		// extract heartbeat metrics
		if info.SinceLastSuccess > 0 {
			metrics.SetValidatorHeartbeatStatus(valAddr, "since_last_success", info.SinceLastSuccess)
		}

		if info.LastAckDuration > 0 {
			metrics.SetValidatorHeartbeatStatus(valAddr, "last_ack_duration", info.LastAckDuration)
		}
	}
}
