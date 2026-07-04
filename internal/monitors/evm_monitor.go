// Package monitors provides monitoring implementations for various Hyperliquid node data sources
package monitors

import (
	"context"
	"encoding/json"
	"fmt"
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
	goSafe("evm", func() {
		<-ctx.Done()
		if contractResolver != nil {
			contractResolver.Shutdown()
		}
	})

	// wait for validator status to be determined
	time.Sleep(60 * time.Second)

	// log a warning if running EVM monitoring on a validator node
	if metrics.IsValidator() {
		logger.WarningComponent("evm", "Running EVM monitoring on a validator node - this may impact performance")
	}

	// use the new unified data source
	evmDataDir := filepath.Join(cfg.NodeHome, "data/evm_block_and_receipts/hourly")
	logger.InfoComponent("evm", "Starting unified EVM monitoring in directory: %s", evmDataDir)

	goSafe("evm", func() {
		// check if directory exists
		if _, err := os.Stat(evmDataDir); os.IsNotExist(err) {
			logger.WarningComponent("evm", "EVM block and receipts directory does not exist: %s", evmDataDir)
			// continue running directory might be created later
		}

		tailStream(ctx, tailStreamOpts{
			component:   "evm",
			name:        "evm block+receipts",
			resolve:     func() (string, error) { return latestHourlyFile(evmDataDir) },
			rescanEvery: 2 * time.Second,
			eofSleep:    250 * time.Millisecond,
			onLine: func(line string) {
				if err := processEVMBlockAndReceiptsLine(line); err != nil {
					errCh <- fmt.Errorf("error processing EVM data line: %w", err)
				}
			},
		})
	})
}

func processEVMBlockAndReceiptsLine(line string) error {
	// element 2 (receipts) regularly dwarfs the block payload and nothing
	// downstream reads it, so it must never be materialized: decode the
	// outer array lazily and only unmarshal elements 0 and 1
	var data []json.RawMessage
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error unmarshaling EVM data: %w", err)
	}

	if len(data) < 2 {
		return fmt.Errorf("invalid data format: expected at least 2 elements, got %d", len(data))
	}

	// element 0: ISO timestamp (e.g., "2025-05-27T12:00:00.602996317")
	var timestampStr string
	if err := json.Unmarshal(data[0], &timestampStr); err != nil {
		return fmt.Errorf("invalid timestamp format: %w", err)
	}

	timestamp, err := time.Parse(time.RFC3339Nano, timestampStr)
	if err != nil {
		// fallback to parsing from block data if ISO timestamp fails
		logger.Debug("failed to parse ISO timestamp, will extract from block: %v", err)
		timestamp = time.Time{}
	}

	var blockData interface{}
	if err := json.Unmarshal(data[1], &blockData); err != nil {
		return fmt.Errorf("invalid block data: %w", err)
	}

	if _, err := processBlockData(blockData, timestamp); err != nil {
		return fmt.Errorf("error processing block data: %w", err)
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
	metrics.MarkMonitorTick("evm")

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
				// Hyperliquid produces blocks at several discrete gas-limit tiers
				// (2M standard, 3M, 30M big, etc). The "other" bucket used to log
				// a WARNING per block, which produced sustained log spam since
				// the 3M tier is common in practice. Categorize known tiers and
				// keep "other" silent (debug only).
				switch {
				case gasLimit <= 2_000_000:
					blockType = "standard"
				case gasLimit == 3_000_000:
					blockType = "small"
				case gasLimit >= 30_000_000:
					blockType = "high"
				default:
					blockType = "other"
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
