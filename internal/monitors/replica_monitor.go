package monitors

import (
	"context"
	"sync"
	"time"

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
		component:   "replica",
		name:        "replica cmds",
		resolve:     func() (string, error) { return utils.LatestReplicaFile(m.dataDir) },
		rescanEvery: 2 * time.Second,
		eofSleep:    25 * time.Millisecond,
		bufSize:     m.bufferSize,
		onLine: func(line string) {
			if len(line) == 0 || line == "\n" {
				return
			}

			m.mu.Lock()
			m.verificationStats.LinesProcessed++
			m.mu.Unlock()

			parseStart := time.Now()
			block, err := m.parser.ParseBlockFromLine([]byte(line))
			totalParseTime += time.Since(parseStart).Seconds()
			parseCount++
			if err != nil {
				logger.DebugComponent("replica", "Error parsing JSON: %v", err)
				m.mu.Lock()
				m.verificationStats.ParseErrors++
				m.mu.Unlock()
				return
			}

			blockMetrics, err := m.parser.ExtractMetrics(block)
			if err != nil {
				logger.DebugComponent("replica", "error extracting metrics: %v", err)
				m.parser.ReturnBlock(block)
				return
			}
			m.parser.ReturnBlock(block)

			m.processBlock(blockMetrics)
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
	})
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
	m.verificationStats.LastBlockHeight = block.Round
	m.verificationStats.ActionsProcessed += int64(block.TotalActions)
	m.verificationStats.OperationsProcessed += int64(block.TotalOperations)

	// update counters
	metrics.IncCoreBlocksProcessed()

	// per-block round advance: 1 on a healthy chain, >1 means skipped
	// (timed-out) rounds between this block and its parent
	if block.ParentRound > 0 && block.Round > block.ParentRound {
		metrics.HLCoreRoundAdvance.Observe(float64(block.Round - block.ParentRound))
	}
	if block.HardforkVersion > 0 {
		metrics.HLCoreHardforkVersion.Set(float64(block.HardforkVersion))
	}

	// update proposer counter
	if block.Proposer != "" {
		metrics.IncrementProposerCounter(block.Proposer)
	}

	// update action counters
	orderCount := int64(0)
	cancelCount := int64(0)
	for actionType, count := range block.ActionCounts {
		metrics.IncCoreTxTotal(actionType, int64(count))

		// track in verification stats
		m.verificationStats.ActionCounts[actionType] += int64(count)

		// track orders separately
		if actionType == replica.ActionTypeOrder || actionType == replica.ActionTypeTwapOrder {
			orderCount += int64(count)
		}
		// track cancels
		if actionType == replica.ActionTypeCancel || actionType == replica.ActionTypeCancelByCloid {
			cancelCount += int64(count)
		}
	}

	// update operation counters (new)
	for actionType, count := range block.OperationCounts {
		category := getCategoryForAction(actionType)
		metrics.IncCoreOperationsTotal(actionType, category, int64(count))
	}

	// update dedicated order counter
	if orderCount > 0 {
		metrics.IncCoreOrdersTotal(orderCount)
	}

	// update histogram
	metrics.ObserveCoreTxPerBlock(float64(block.TotalActions))
	metrics.ObserveCoreOrdersPerBlock(float64(orderCount))

	// update operations histogram (new)
	metrics.ObserveCoreOperationsPerBlock(float64(block.TotalOperations))

	// update last processed metrics
	metrics.SetCoreLastProcessedRound(float64(block.Round))
	metrics.SetCoreLastProcessedTime(float64(block.Time.Unix()))
	metrics.MarkMonitorTick("replica")
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
	switch actionType {
	// trading operations
	case replica.ActionTypeOrder, replica.ActionTypeTwapOrder,
		replica.ActionTypeCancel, replica.ActionTypeCancelByCloid,
		replica.ActionTypeBatchModify, replica.ActionTypeModify,
		replica.ActionTypeScheduleCancel,
		"liquidate", "twapCancel":
		return "trading"

	// transfer operations
	case replica.ActionTypeUsdTransfer, replica.ActionTypeSpotSend,
		"usdSend", "spotDeploy", "withdraw3", "spotUser",
		"vaultTransfer", "vaultDistribute", "subAccountTransfer",
		"subAccountSpotTransfer", "cDeposit", "cWithdraw":
		return "transfer"

	// settings/account operations
	case replica.ActionTypeUpdateLeverage, replica.ActionTypeApproveAgent,
		replica.ActionTypeSetReferrer, replica.ActionTypeApproveBuilderFee,
		"createSubAccount", "subAccountModify", "registerReferrer",
		"linkStakingUser", "setDisplayName", "createVault",
		"vaultModify", "updateIsolatedMargin", "topUpIsolatedOnlyMargin",
		"convertToMultiSigUser", "multiSig":
		return "settings"

	// governance/system operations
	case replica.ActionTypeTokenDelegate, replica.ActionTypeVoteAppHash,
		"VoteEthDepositAction", "VoteEthFinalizedWithdrawalAction",
		"VoteGlobalAction", "SetGlobalAction", "CSignerAction",
		"ValidatorSignWithdrawalAction", "NetChildVaultPositionsAction":
		return "governance"

	// rewards/claiming
	case replica.ActionTypeClaimRewards, "reserveRequestWeight":
		return "rewards"

	// evm operations
	case replica.ActionTypeEvmRawTx, replica.ActionTypeEvmUserModify,
		"evmUserSpotTransfer", "finalizeEvmContract":
		return "evm"

	default:
		return "other"
	}
}
