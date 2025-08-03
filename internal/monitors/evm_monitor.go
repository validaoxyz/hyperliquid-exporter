// Package monitors provides monitoring implementations for various Hyperliquid node data sources
package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/cache"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/contracts"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

var (
	// track the last block time for calculating block time differences
	lastEVMBlockTime time.Time

	// contract tracking for per-contract metrics - using LRU cache
	contractCache          *cache.LRUCache
	contractMetricsEnabled bool
	contractLimit          int

	// contract resolver for name resolution
	contractResolver *contracts.Resolver

	// block type metrics flag
	blockTypeMetricsEnabled bool
)

// 1. Reads from evm_block_and_receipts files
// 2. Parses height, gas, fees, success, gas usage
// 3. Updates all EVM-related prometheus metrics
func StartEVMMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// initialize config
	contractMetricsEnabled = cfg.EnableContractMetrics
	contractLimit = cfg.ContractMetricsLimit
	blockTypeMetricsEnabled = cfg.EVMBlockTypeMetrics

	logger.InfoComponent("evm", "EVM monitor config: contractMetrics=%v, blockTypeMetrics=%v",
		contractMetricsEnabled, blockTypeMetricsEnabled)

	// initialize contract cache with LRU eviction
	if contractMetricsEnabled {
		// use configured limit or default to 1000 contracts, 24 hour TTL
		cacheSize := contractLimit
		if cacheSize <= 0 {
			cacheSize = 1000
		}
		contractCache = cache.NewLRUCache(cacheSize, 24*time.Hour)
		logger.InfoComponent("evm", "Initialized contract cache with size %d", cacheSize)
	}

	// initialize contract resolver
	contractResolver = contracts.NewResolver()
	if err := contractResolver.Initialize(ctx); err != nil {
		logger.WarningComponent("evm", "Failed to initialize contract resolver: %v", err)
		// continue anyway - we can still track with unknown names
	}

	// ensure resolver is cleaned up when context is cancelled
	go func() {
		<-ctx.Done()
		if contractResolver != nil {
			contractResolver.Shutdown()
		}
	}()

	// wait for validator status to be determined
	time.Sleep(60 * time.Second)

	// log a warning if running EVM monitoring on a validator node
	if metrics.IsValidator() {
		logger.WarningComponent("evm", "Running EVM monitoring on a validator node - this may impact performance")
	}

	// use the new unified data source
	evmDataDir := filepath.Join(cfg.NodeHome, "data/evm_block_and_receipts/hourly")
	logger.InfoComponent("evm", "Starting unified EVM monitoring in directory: %s", evmDataDir)

	go func() {
		// check if directory exists
		if _, err := os.Stat(evmDataDir); os.IsNotExist(err) {
			logger.WarningComponent("evm", "EVM block and receipts directory does not exist: %s", evmDataDir)
			// continue running directory might be created later
		}

		var currentFilePath string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(1 * time.Second)

				// find the latest file in the directory
				latestFile, err := utils.GetLatestFile(evmDataDir)
				if err != nil {
					errCh <- fmt.Errorf("error finding latest EVM data file: %w", err)
					continue
				}

				// switch to new file if needed
				if latestFile != currentFilePath {
					logger.InfoComponent("evm", "Switching to new EVM data file: %s", latestFile)

					if fileReader != nil {
						fileReader = nil
					}

					file, err := os.Open(latestFile)
					if err != nil {
						errCh <- fmt.Errorf("error opening EVM data file: %w", err)
						continue
					}

					// on first run start from eof (tail mode)
					// on subsequent runs read entire file
					if isFirstRun {
						_, err = file.Seek(0, io.SeekEnd)
						if err != nil {
							errCh <- fmt.Errorf("error seeking to end of file: %w", err)
							file.Close()
							continue
						}
						logger.InfoComponent("evm", "First run: starting to stream from the end of file %s", latestFile)
						isFirstRun = false
					} else {
						logger.InfoComponent("evm", "Not first run: reading entire file %s", latestFile)
					}

					fileReader = bufio.NewReader(file)
					currentFilePath = latestFile
				}

				// process lines from the file
				if fileReader != nil {
					for {
						line, err := fileReader.ReadString('\n')
						if err != nil {
							if err == io.EOF {
								break
							}
							errCh <- fmt.Errorf("error reading from EVM data file: %w", err)
							break
						}

						if err := processEVMBlockAndReceiptsLine(line); err != nil {
							errCh <- fmt.Errorf("error processing EVM data line: %w", err)
							continue
						}
					}
				}
			}
		}
	}()
}

func processEVMBlockAndReceiptsLine(line string) error {
	var data []interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error unmarshaling EVM data: %w", err)
	}

	if len(data) < 2 {
		return fmt.Errorf("invalid data format: expected at least 2 elements, got %d", len(data))
	}

	// element 0: ISO timestamp (e.g., "2025-05-27T12:00:00.602996317")
	timestampStr, ok := data[0].(string)
	if !ok {
		return fmt.Errorf("invalid timestamp format: expected string, got %T", data[0])
	}

	timestamp, err := time.Parse(time.RFC3339Nano, timestampStr)
	if err != nil {
		// fallback to parsing from block data if ISO timestamp fails
		logger.Debug("failed to parse ISO timestamp, will extract from block: %v", err)
		timestamp = time.Time{}
	}

	blockData := data[1]

	var receiptsData interface{}
	if len(data) >= 3 {
		receiptsData = data[2]
		logger.DebugComponent("evm", "Line has receipts data: %v", receiptsData != nil)
	} else {
		logger.DebugComponent("evm", "Line has no receipts data (only %d elements)", len(data))
	}

	blockType, err := processBlockData(blockData, timestamp)
	if err != nil {
		return fmt.Errorf("error processing block data: %w", err)
	}

	if receiptsData != nil {
		logger.DebugComponent("evm", "Processing receipts data")
		if err := processReceiptsData(receiptsData, blockType); err != nil {
			// Log but don't fail - receipts might be null for empty blocks
			logger.Debug("error processing receipts: %v", err)
		}
	} else {
		logger.DebugComponent("evm", "No receipts data to process")
	}

	return nil
}

func processBlockData(blockData interface{}, isoTimestamp time.Time) (string, error) {
	blockMap, ok := blockData.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid block data format")
	}

	block, ok := blockMap["block"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("missing block field")
	}

	// support different block formats
	var blockContent map[string]interface{}
	for _, v := range block {
		blockContent, ok = v.(map[string]interface{})
		if ok {
			break
		}
	}

	if blockContent == nil {
		return "", fmt.Errorf("no valid block content found")
	}

	// extract header info
	headerWrapper, ok := blockContent["header"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("missing header field")
	}

	header, ok := headerWrapper["header"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("missing inner header field")
	}

	blockNumberHex, ok := header["number"].(string)
	if !ok {
		return "", fmt.Errorf("missing block number")
	}

	blockNumber, err := strconv.ParseInt(strings.TrimPrefix(blockNumberHex, "0x"), 16, 64)
	if err != nil {
		return "", fmt.Errorf("invalid block number: %w", err)
	}

	// set block height metric
	metrics.SetEVMBlockHeight(blockNumber)

	// determine block type early so we can use it throughout
	var blockType string
	var gasLimit int64
	if gasLimitHex, ok := header["gasLimit"].(string); ok {
		gasLimit, err = strconv.ParseInt(strings.TrimPrefix(gasLimitHex, "0x"), 16, 64)
		if err == nil {
			// gas limit distribution
			metrics.RecordGasLimitDistribution(float64(gasLimit))

			// track high gas limit blocks
			if gasLimit >= 30_000_000 {
				metrics.IncrementHighGasLimitBlocks("30m")
				// extract gas used for high gas block tracking
				var gasUsed int64
				if gasUsedHex, ok := header["gasUsed"].(string); ok {
					gasUsed, _ = strconv.ParseInt(strings.TrimPrefix(gasUsedHex, "0x"), 16, 64)
				}
				metrics.SetLastHighGasBlock(blockNumber, gasLimit, gasUsed, isoTimestamp)
			}

			metrics.UpdateMaxGasLimit(gasLimit)

			if blockTypeMetricsEnabled {
				if gasLimit <= 2_000_000 {
					blockType = "standard"
				} else if gasLimit >= 30_000_000 {
					blockType = "high"
				} else {
					blockType = "other"
					logger.WarningComponent("evm", "Unexpected gas limit: %d", gasLimit)
				}
				logger.DebugComponent("evm", "Block %d: gasLimit=%d, blockType=%s", blockNumber, gasLimit, blockType)
			}
		}
	}

	var blockTimestamp time.Time
	if !isoTimestamp.IsZero() {
		blockTimestamp = isoTimestamp
	} else {
		// fallback to hex timestamp from header
		if timestampHex, ok := header["timestamp"].(string); ok {
			ts, err := strconv.ParseInt(strings.TrimPrefix(timestampHex, "0x"), 16, 64)
			if err == nil {
				blockTimestamp = time.Unix(ts, 0).UTC()
			}
		}
	}

	if !blockTimestamp.IsZero() {
		// calculate block time difference
		if !lastEVMBlockTime.IsZero() {
			diffMs := blockTimestamp.Sub(lastEVMBlockTime).Milliseconds()
			if diffMs > 0 {
				metrics.RecordEVMBlockTime(float64(diffMs))
			}
		}
		lastEVMBlockTime = blockTimestamp
		metrics.SetEVMLatestBlockTime(blockTimestamp.Unix())
	}

	// extract gas metrics
	if gasUsedHex, ok := header["gasUsed"].(string); ok {
		gasUsed, err := strconv.ParseInt(strings.TrimPrefix(gasUsedHex, "0x"), 16, 64)
		if err == nil && gasLimit > 0 {
			// use the block type determined earlier
			if blockTypeMetricsEnabled && blockType != "" {
				metrics.SetEVMGasUsage(gasUsed, gasLimit, blockType)
			} else {
				metrics.SetEVMGasUsage(gasUsed, gasLimit)
			}
		}
	}

	// extract base fee
	if baseFeeHex, ok := header["baseFeePerGas"].(string); ok {
		baseFeeWei, err := strconv.ParseInt(strings.TrimPrefix(baseFeeHex, "0x"), 16, 64)
		if err == nil && baseFeeWei > 0 {
			baseFeeGwei := float64(baseFeeWei) / 1e9
			if blockTypeMetricsEnabled && blockType != "" {
				metrics.SetEVMBaseFeeGwei(baseFeeGwei, blockType)
			} else {
				metrics.SetEVMBaseFeeGwei(baseFeeGwei)
			}
		}
	}

	// process transactions
	if body, ok := blockContent["body"].(map[string]interface{}); ok {
		if err := processTransactions(body, blockType); err != nil {
			logger.Debug("error processing transactions: %v", err)
		}
	}

	return blockType, nil
}

// extracts transaction metrics from the block body
func processTransactions(body map[string]interface{}, blockType string) error {
	transactions, ok := body["transactions"].([]interface{})
	if !ok {
		return nil // No transactions in block
	}

	txCount := len(transactions)
	if txCount > 0 {
		if blockTypeMetricsEnabled && blockType != "" {
			metrics.RecordEVMTxPerBlock(txCount, blockType)
		} else {
			metrics.RecordEVMTxPerBlock(txCount)
		}
	}

	var maxPriorityFeeWei int64

	for _, tx := range transactions {
		txMap, ok := tx.(map[string]interface{})
		if !ok {
			continue
		}

		// extract transaction details
		if transaction, ok := txMap["transaction"].(map[string]interface{}); ok {
			// process transaction type and details
			for txType, txData := range transaction {
				if blockTypeMetricsEnabled && blockType != "" {
					metrics.IncrementEVMTxType(txType, blockType)
				} else {
					metrics.IncrementEVMTxType(txType)
				}

				if txDataMap, ok := txData.(map[string]interface{}); ok {
					// check for contract creation (empty or zero 'to' address)
					if to, ok := txDataMap["to"].(string); ok {
						addr := strings.ToLower(to)
						if addr == "" || strings.TrimPrefix(addr, "0x") == "" ||
							strings.TrimPrefix(addr, "0x") == strings.Repeat("0", 40) {
							if blockTypeMetricsEnabled && blockType != "" {
								metrics.IncrementEVMContractCreations(blockType)
							} else {
								metrics.IncrementEVMContractCreations()
							}
						} else if contractMetricsEnabled && contractCache != nil {
							// track contract interactions using LRU cache
							var contractInfo *contracts.ContractInfo

							// check if already in cache
							if cachedInfo, found := contractCache.Get(addr); found {
								contractInfo = cachedInfo.(*contracts.ContractInfo)
							} else {
								// get contract info from resolver
								if contractResolver != nil {
									contractInfo = contractResolver.GetContractInfo(addr)
								}

								// default values if resolver is not available
								if contractInfo == nil {
									contractInfo = &contracts.ContractInfo{
										Address: addr,
										Name:    "unknown",
										IsToken: false,
										Type:    "unknown",
										Symbol:  "",
									}
								}

								// add to cache
								contractCache.Set(addr, contractInfo)
							}

							if blockTypeMetricsEnabled && blockType != "" {
								metrics.IncrementEVMContractTx(
									contractInfo.Address,
									contractInfo.Name,
									contractInfo.IsToken,
									contractInfo.Type,
									contractInfo.Symbol,
									blockType,
								)
							} else {
								metrics.IncrementEVMContractTx(
									contractInfo.Address,
									contractInfo.Name,
									contractInfo.IsToken,
									contractInfo.Type,
									contractInfo.Symbol,
								)
							}
						}
					}

					// extract max priority fee for EIP-1559 transactions
					if txType == "Eip1559" {
						if maxPriorityFeeHex, ok := txDataMap["maxPriorityFeePerGas"].(string); ok {
							fee, err := strconv.ParseInt(strings.TrimPrefix(maxPriorityFeeHex, "0x"), 16, 64)
							if err == nil && fee > maxPriorityFeeWei {
								maxPriorityFeeWei = fee
							}
						}
					}
				}
			}
		}
	}

	// set max priority fee metric
	if maxPriorityFeeWei > 0 {
		maxPriorityFeeGwei := float64(maxPriorityFeeWei) / 1e9
		if blockTypeMetricsEnabled && blockType != "" {
			metrics.SetEVMMaxPriorityFeeGwei(maxPriorityFeeGwei, blockType)
		} else {
			metrics.SetEVMMaxPriorityFeeGwei(maxPriorityFeeGwei)
		}
	}

	return nil
}

func processReceiptsData(receiptsData interface{}, blockType string) error {
	receiptsMap, ok := receiptsData.(map[string]interface{})
	if !ok {
		logger.DebugComponent("evm", "Receipts data is not a map: %T", receiptsData)
		return fmt.Errorf("invalid receipts format")
	}

	receipts, ok := receiptsMap["receipts"].([]interface{})
	if !ok {
		logger.DebugComponent("evm", "No receipts array in receipts map")
		return nil // No receipts
	}

	logger.DebugComponent("evm", "Found %d receipts (likely 0 due to null data)", len(receipts))

	return nil
}
