package monitors

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
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

	go m.streamLoop(ctx)
	return nil
}

// continuously streams from the latest replica file
func (m *ReplicaMonitor) streamLoop(ctx context.Context) {
	var currentFile string
	var file *os.File
	var fileReader *bufio.Reader
	isFirstRun := true

	// wait a short time at startup to ensure signer mappings are populated
	// prevents early blocks from having incorrect validator labels
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		logger.DebugComponent("replica", "Initial startup delay complete, beginning processing")
	}

	defer func() {
		if file != nil {
			file.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// check for latest file
			latestFile, err := utils.GetLatestFile(m.dataDir)
			if err != nil {
				logger.ErrorComponent("replica", "Error finding latest replica file: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}

			// if a new file is found, switch to it
			if latestFile != currentFile {
				logger.InfoComponent("replica", "Switching to replica file: %s", latestFile)

				// clean up old file
				if file != nil {
					file.Close()
				}

				file, err = os.Open(latestFile)
				if err != nil {
					logger.ErrorComponent("replica", "Error opening replica file: %v", err)
					time.Sleep(1 * time.Second)
					continue
				}

				if isFirstRun {
					// on first run, seek to the end of the file
					_, err = file.Seek(0, io.SeekEnd)
					if err != nil {
						logger.ErrorComponent("replica", "Error seeking to end of file: %v", err)
						file.Close()
						file = nil
						time.Sleep(1 * time.Second)
						continue
					}
					logger.InfoComponent("replica", "First run: starting to stream from the end of file %s", latestFile)
				} else {
					logger.InfoComponent("replica", "Not first run: reading entire file %s", latestFile)
				}

				fileReader = bufio.NewReaderSize(file, m.bufferSize)
				currentFile = latestFile
				isFirstRun = false
			}

			// read and process lines
			if fileReader != nil {
				blocksProcessed := 0
				var totalParseTime float64
				var parseCount int

				for {
					line, err := fileReader.ReadString('\n')
					if err != nil {
						if err == io.EOF {
							// end of file reached, wait a bit before checking for more data
							if blocksProcessed > 0 {
								logger.DebugComponent("replica", "Processed %d blocks, waiting for more data", blocksProcessed)
								blocksProcessed = 0
							}
							// update parse duration metric with average
							if parseCount > 0 {
								avgParseTime := totalParseTime / float64(parseCount)
								metrics.SetReplicaParseDuration(avgParseTime)
							}
							time.Sleep(10 * time.Millisecond)
							break // break from inner loop, continue outer loop
						}
						// for other errors, log and break to outer loop
						logger.ErrorComponent("replica", "Error reading line: %v", err)
						break
					}

					// skip empty lines
					if len(line) == 0 || line == "\n" {
						continue
					}

					// update line count
					m.mu.Lock()
					m.verificationStats.LinesProcessed++
					m.mu.Unlock()

					// parse the JSON line into a replica block using the parser's pool
					parseStart := time.Now()
					block, err := m.parser.ParseBlockFromLine([]byte(line))
					parseTime := time.Since(parseStart).Seconds()
					totalParseTime += parseTime
					parseCount++
					if err != nil {
						logger.DebugComponent("replica", "Error parsing JSON: %v", err)
						m.mu.Lock()
						m.verificationStats.ParseErrors++
						m.mu.Unlock()
						continue
					}

					// extract metrics from the block
					metrics, err := m.parser.ExtractMetrics(block)
					if err != nil {
						logger.DebugComponent("replica", "error extracting metrics: %v", err)
						// return block to pool even on error
						m.parser.ReturnBlock(block)
						continue
					}

					// return block to pool after processing
					m.parser.ReturnBlock(block)

					// process the metrics
					m.processBlock(metrics)
					blocksProcessed++
					if blocksProcessed%100 == 0 {
						logger.InfoComponent("replica", "Processed %d blocks so far", blocksProcessed)
					}
				}
			}
		}
	}
}

// processes a single replica block
func (m *ReplicaMonitor) processReplicaBlock(block *replica.ReplicaBlock) error {
	// extract metrics from the block
	metrics, err := m.parser.ExtractMetrics(block)
	if err != nil {
		return fmt.Errorf("extract metrics: %w", err)
	}

	m.processBlock(metrics)
	return nil
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
	metrics.IncCoreRoundsProcessed()

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

	// update operations histogram (new)
	metrics.ObserveCoreOperationsPerBlock(float64(block.TotalOperations))

	// update last processed metrics
	metrics.SetCoreLastProcessedRound(float64(block.Round))
	metrics.SetCoreLastProcessedTime(float64(block.Time.Unix()))

	// also update implementation-specific metrics
	metrics.SetReplicaLastProcessedRound(float64(block.Round))
	metrics.SetReplicaLastProcessedTime(float64(block.Time.Unix()))
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
