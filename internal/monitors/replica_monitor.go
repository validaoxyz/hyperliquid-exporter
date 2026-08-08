package monitors

import (
	"context"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/actiontypes"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/replica"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

// replica verification stats
type ReplicaVerificationStats struct {
	LinesProcessed      int64
	BlocksProcessed     int64
	ActionsProcessed    int64
	OperationsProcessed int64
	ParseErrors         int64
	LastProcessedAt     time.Time
	LastBlockHeight     int64
	ActionCounts        map[string]int64
}

// replica monitor
type ReplicaMonitor struct {
	parser     *replica.Parser
	dataDir    string
	bufferSize int
	mu         sync.RWMutex

	// Metrics tracking
	lastRound int64
	lastTime  time.Time

	// Verification tracking
	verificationStats ReplicaVerificationStats
}

// creates a new streaming replica monitor
func NewReplicaMonitor(dataDir string, bufferSize int) *ReplicaMonitor {
	return &ReplicaMonitor{
		parser:     replica.NewParser(bufferSize),
		dataDir:    dataDir,
		bufferSize: bufferSize * 1024 * 1024, // Convert MB to bytes
		verificationStats: ReplicaVerificationStats{
			ActionCounts: make(map[string]int64),
		},
	}
}

// starts monitoring replica files using streaming approach
func (m *ReplicaMonitor) Start(ctx context.Context) error {
	metrics.RegisterSource(metrics.SourceReplica, true)
	logger.InfoComponent("replica", "Starting streaming replica monitor, dataDir: %s", m.dataDir)

	goSafe("replica", func() { m.streamLoop(ctx) })
	return nil
}

// continuously streams from the latest replica file
func (m *ReplicaMonitor) streamLoop(ctx context.Context) {
	// wait a short time at startup to ensure signer mappings are populated
	// prevents early blocks from having incorrect validator labels
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		logger.DebugComponent("replica", "Initial startup delay complete, beginning processing")
	}

	blocksProcessed := 0
	var totalParseTime float64
	var parseCount int

	tailStream(ctx, tailStreamOpts{
		component: "replica",
		name:      "replica cmds",
		resolveState: func() tailStreamResolution {
			metrics.MarkMonitorAttempt("replica")
			metrics.MarkSourceAttempt(metrics.SourceReplica)
			path, rootMissing, err := utils.LatestReplicaFileStrict(m.dataDir)
			if err != nil {
				metrics.MarkSourceError(metrics.SourceReplica, metrics.SourceFailureDiscovery)
				return tailStreamResolution{err: err}
			}
			if rootMissing {
				return tailStreamResolution{unavailable: tailStreamUnavailableAbsent}
			}
			if path == "" {
				return tailStreamResolution{unavailable: tailStreamUnavailableEmpty}
			}
			return tailStreamResolution{path: path}
		},
		rescanEvery: 2 * time.Second,
		eofSleep:    25 * time.Millisecond,
		bufSize:     m.bufferSize,
		onLine: func(line string) {
			if len(line) == 0 || line == "\n" {
				return
			}
			metrics.MarkMonitorAttempt("replica")
			metrics.MarkSourceAttempt(metrics.SourceReplica)

			m.mu.Lock()
			m.verificationStats.LinesProcessed++
			m.mu.Unlock()

			parseStart := time.Now()
			block, err := m.parser.ParseBlockFromLine([]byte(line))
			totalParseTime += time.Since(parseStart).Seconds()
			parseCount++
			if err != nil {
				logger.DebugComponent("replica", "Error parsing JSON: %v", err)
				m.recordParseFailure(err)
				return
			}

			blockMetrics, err := m.parser.ExtractMetrics(block)
			if err != nil {
				logger.DebugComponent("replica", "error extracting metrics: %v", err)
				m.parser.ReturnBlock(block)
				m.recordParseFailure(err)
				return
			}
			m.parser.ReturnBlock(block)

			m.commitBlock(blockMetrics)
			blocksProcessed++
		},
		onIdle: func() {
			if blocksProcessed > 0 {
				logger.DebugComponent("replica", "Processed %d blocks, waiting for more data", blocksProcessed)
				blocksProcessed = 0
			}
			if parseCount > 0 {
				metrics.SetReplicaParseDuration(totalParseTime / float64(parseCount))
				totalParseTime, parseCount = 0, 0
			}
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(metrics.SourceReplica, true)
		},
		onUnavailable: func(kind tailStreamUnavailable) {
			totalParseTime, parseCount = 0, 0
			m.commitUnavailable(kind)
		},
		onFailure: func(failure tailStreamFailure) {
			markTailSourceFailure(metrics.SourceReplica, failure)
			metrics.IncMonitorError("replica")
		},
	})
}

func (m *ReplicaMonitor) commitBlock(block *replica.BlockMetrics) {
	commitReplicaGeneration(func() {
		m.processBlock(block)
		metrics.MarkSourceValidObservation(metrics.SourceReplica, block.Time)
		metrics.MarkSourcePublication(metrics.SourceReplica)
		metrics.MarkMonitorValidObservation("replica")
		metrics.MarkMonitorPublication("replica")
	})
}

func commitReplicaGeneration(publish func()) {
	metrics.WithPrometheusSnapshotUpdate(publish)
}

func (m *ReplicaMonitor) commitUnavailable(kind tailStreamUnavailable) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.WithdrawReplicaCurrentSnapshot()
		if kind == tailStreamUnavailableAbsent {
			metrics.MarkSourceAbsent(metrics.SourceReplica)
		} else {
			metrics.MarkSourceAvailable(metrics.SourceReplica)
		}
		metrics.MarkSourcePublication(metrics.SourceReplica)
		metrics.MarkMonitorPublication("replica")
	})
}

func (m *ReplicaMonitor) recordParseFailure(err error) {
	stage, reason := replica.ClassifyError(err)
	metrics.HLReplicaParserEventsTotal.WithLabelValues(stage, reason).Inc()
	metrics.MarkSourceError(metrics.SourceReplica, metrics.SourceFailureSchema)
	metrics.IncMonitorError("replica")
	m.mu.Lock()
	m.verificationStats.ParseErrors++
	m.mu.Unlock()
}

// processes metrics from a single block
func (m *ReplicaMonitor) processBlock(block *replica.BlockMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// update last processed info
	m.lastRound = block.Round
	m.lastTime = block.Time

	// update verification stats
	m.verificationStats.BlocksProcessed++
	m.verificationStats.LastProcessedAt = time.Now()
	m.verificationStats.LastBlockHeight = block.Height
	m.verificationStats.ActionsProcessed += int64(block.TotalActions)
	m.verificationStats.OperationsProcessed += int64(block.TotalOperations)

	// update counters
	metrics.IncCoreBlocksProcessed()
	metrics.HLReplicaActionBundlesTotal.Add(float64(block.TotalBundles))
	metrics.SetReplicaLastProcessedHeight(float64(block.Height))

	// Positive round - parent_round from the accepted replica record.
	if block.ParentRound > 0 && block.Round > block.ParentRound {
		metrics.HLCoreRoundAdvance.Observe(float64(block.Round - block.ParentRound))
	}
	// update proposer counter
	if block.Proposer != "" {
		metrics.IncrementProposerCounter(block.Proposer)
	}

	// update action counters
	orderCount := int64(0)
	for actionType, count := range block.ActionCounts {
		metrics.IncCoreTxTotal(actionType, int64(count))
		metrics.HLReplicaSignedActionsTotal.WithLabelValues(actionType).Add(float64(count))

		// track in verification stats
		m.verificationStats.ActionCounts[actionType] += int64(count)

		// track orders separately
	}

	// update operation counters (new)
	for actionType, count := range block.OperationCounts {
		category := actiontypes.Category(actionType)
		metrics.IncCoreOperationsTotal(actionType, category, int64(count))
		metrics.HLReplicaOperationsTotal.WithLabelValues(actionType, category).Add(float64(count))
		if actionType == replica.ActionTypeOrder || actionType == replica.ActionTypeTwapOrder {
			orderCount += int64(count)
		}
	}

	// update dedicated order counter
	if orderCount > 0 {
		metrics.IncCoreOrdersTotal(orderCount)
		metrics.HLReplicaOrdersTotal.Add(float64(orderCount))
	}
	for category, count := range block.MultiSigInner {
		metrics.HLReplicaMultiSigInnerActionsTotal.WithLabelValues(category).Add(float64(count))
	}
	for reason, count := range block.ParserEvents {
		stage := "action"
		if reason == "multisig_inner_missing" || reason == "unknown_multisig_inner" {
			stage = "multisig"
		}
		metrics.HLReplicaParserEventsTotal.WithLabelValues(stage, reason).Add(float64(count))
	}
	publishReplicaResponses(block.Responses)

	// update histogram
	metrics.ObserveCoreTxPerBlock(float64(block.TotalActions))
	metrics.ObserveCoreOrdersPerBlock(float64(orderCount))

	// update operations histogram (new)
	metrics.ObserveCoreOperationsPerBlock(float64(block.TotalOperations))

	// update last processed metrics
	metrics.SetCoreLastProcessedRound(float64(block.Round))
	metrics.SetCoreLastProcessedTime(float64(block.Time.Unix()))
}

func publishReplicaResponses(response replica.ResponseMetrics) {
	metrics.HLReplicaResponseCoverageTotal.WithLabelValues(response.Coverage).Inc()
	metrics.HLReplicaResponseCountRelationTotal.WithLabelValues(response.CountRelation).Inc()
	validRecords := response.Records - response.MalformedRecords
	if validRecords > 0 {
		metrics.HLReplicaResponseRecordsTotal.WithLabelValues("valid").Add(float64(validRecords))
	}
	if response.MalformedRecords > 0 {
		metrics.HLReplicaResponseRecordsTotal.WithLabelValues("malformed").Add(float64(response.MalformedRecords))
	}
	if response.MalformedContainers > 0 {
		metrics.HLReplicaParserEventsTotal.WithLabelValues("response", "malformed_container").Add(float64(response.MalformedContainers))
	}
	for status, count := range response.ActionStatuses {
		metrics.HLReplicaResponseActionStatusTotal.WithLabelValues(status).Add(float64(count))
	}
	for outcome, count := range response.Outcomes {
		metrics.HLReplicaExecutionOutcomesTotal.WithLabelValues(outcome).Add(float64(count))
	}
}

// returns current statistics
func (m *ReplicaMonitor) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := map[string]interface{}{
		"last_round": m.lastRound,
		"last_time":  m.lastTime,
	}

	return stats
}

// returns current verification stats
func (m *ReplicaMonitor) GetVerificationStats() ReplicaVerificationStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// create a copy to avoid race conditions
	statsCopy := m.verificationStats
	statsCopy.ActionCounts = make(map[string]int64)
	for k, v := range m.verificationStats.ActionCounts {
		statsCopy.ActionCounts[k] = v
	}

	return statsCopy
}

// returns the category for a given action type
func getCategoryForAction(actionType string) string {
	return actiontypes.Category(actionType)
}
