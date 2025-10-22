package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/cache"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

// track last block time separately for fast and slow states
var (
	lastBlockTimeMu sync.RWMutex
	lastBlockTimes  *cache.LRUCache
	// keep track of block heights for cleanup
	blockHeightsByState *cache.LRUCache
	maxBlockHistory     = 100
)

func StartBlockMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// init LRU caches
	lastBlockTimes = cache.NewLRUCache(10, 0)      // No TTL, just size limit
	blockHeightsByState = cache.NewLRUCache(10, 0) // No TTL, just size limit

	// check if new dual-state directories exist
	fastDir := filepath.Join(cfg.NodeHome, "data", "node_fast_block_times")
	slowDir := filepath.Join(cfg.NodeHome, "data", "node_slow_block_times")
	oldDir := filepath.Join(cfg.NodeHome, "data", "block_times")

	fastExists := false
	slowExists := false
	oldExists := false

	if _, err := os.Stat(fastDir); err == nil {
		fastExists = true
	}
	if _, err := os.Stat(slowDir); err == nil {
		slowExists = true
	}
	if _, err := os.Stat(oldDir); err == nil {
		oldExists = true
	}

	// if new directories exist, use dual-state monitoring
	if fastExists || slowExists {
		logger.InfoComponent("core", "Detected new dual-state block time directories")
		if fastExists {
			go monitorBlockState(ctx, cfg, errCh, "fast", "node_fast_block_times")
		}
		if slowExists {
			go monitorBlockState(ctx, cfg, errCh, "slow", "node_slow_block_times")
		}
	} else if oldExists {
		// fallback to old single-directory format for backward compatibility
		logger.InfoComponent("core", "Using legacy single block_times directory (node not yet upgraded)")
		go monitorLegacyBlockState(ctx, cfg, errCh)
	} else {
		logger.WarningComponent("core", "No block time directories found - block monitoring disabled")
	}
}

func monitorBlockState(ctx context.Context, cfg config.Config, errCh chan<- error, stateType string, dirName string) {
	blockTimeDir := filepath.Join(cfg.NodeHome, "data", dirName)
	var currentFile string
	var fileReader *bufio.Reader
	isFirstRun := true

	logger.InfoComponent("core", "Starting %s state block monitor for directory: %s", stateType, blockTimeDir)

	if _, err := os.Stat(blockTimeDir); os.IsNotExist(err) {
		logger.WarningComponent("core", "Block time directory %s does not exist (node may be on older version), monitor disabled", blockTimeDir)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// check for new files
			latestFile, err := utils.GetLatestFile(blockTimeDir)
			if err != nil {
				errCh <- fmt.Errorf("error finding latest %s block time file: %w", stateType, err)
				time.Sleep(1 * time.Second)
				continue
			}

			// if a new file is found, switch to it
			if latestFile != currentFile {
				logger.InfoComponent("core", "Switching to new %s block time file: %s", stateType, latestFile)
				if fileReader != nil {
					fileReader = nil // allow the old file to be garbage collected
				}
				file, err := os.Open(latestFile)
				if err != nil {
					errCh <- fmt.Errorf("error opening new %s block time file: %w", stateType, err)
					time.Sleep(1 * time.Second)
					continue
				}

				if isFirstRun {
					// on first run, seek to the end of the file
					_, err = file.Seek(0, io.SeekEnd)
					if err != nil {
						errCh <- fmt.Errorf("error seeking to end of file: %w", err)
						file.Close()
						time.Sleep(1 * time.Second)
						continue
					}
					logger.InfoComponent("core", "First run: starting to stream %s state from the end of file %s", stateType, latestFile)
				} else {
					logger.InfoComponent("core", "Not first run: reading entire %s state file %s", stateType, latestFile)
				}

				fileReader = bufio.NewReader(file)
				currentFile = latestFile
				isFirstRun = false
			}

			// read and process lines
			for {
				line, err := fileReader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						// end of file reached, wait a bit before checking for more data
						time.Sleep(10 * time.Millisecond)
						break
					}
					errCh <- fmt.Errorf("error reading from %s block time file: %w", stateType, err)
					break
				}
				// Skip empty lines
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				if err := parseBlockTimeLine(ctx, line, stateType); err != nil {
					// Skip invalid lines silently - these are likely partial writes
					// The next read cycle will get the complete line
					logger.DebugComponent("core", "Skipping potentially incomplete %s block time line: %v", stateType, err)
				}
			}
		}
	}
}

func parseBlockTimeLine(ctx context.Context, line string, stateType string) error {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error parsing block time line: %w", err)
	}

	height, ok := data["height"].(float64)
	if !ok {
		return fmt.Errorf("height not found or not a number")
	}

	blockTime, ok := data["block_time"].(string)
	if !ok {
		return fmt.Errorf("block time not found or not a string")
	}

	applyDuration, ok := data["apply_duration"].(float64)
	if !ok {
		return fmt.Errorf("apply duration not found or not a number")
	}

	// new field: begin_block_wall_time (when the block processing started)
	beginBlockWallTime, _ := data["begin_block_wall_time"].(string)

	// convert applyDuration from seconds to milliseconds
	applyDurationMs := applyDuration * 1000

	// parse block_time to Unix timestamp
	layout := "2006-01-02T15:04:05.999999999"
	parsedTime, err := time.Parse(layout, blockTime)
	if err != nil {
		return fmt.Errorf("error parsing block time: %w", err)
	}

	// assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()

	// calculate block time difference for this state type
	lastBlockTimeMu.Lock()
	lastTimeIface, exists := lastBlockTimes.Get(stateType)
	if exists {
		lastTime := lastTimeIface.(time.Time)
		if !lastTime.IsZero() {
			blockTimeDiff := parsedTime.Sub(lastTime).Milliseconds()
			if blockTimeDiff > 0 {
				metrics.RecordBlockTimeWithLabel(float64(blockTimeDiff), stateType)
				logger.DebugComponent("core", "%s state block time difference: %d milliseconds", stateType, blockTimeDiff)
			} else {
				logger.WarningComponent("core", "Invalid %s state block time difference: %d milliseconds", stateType, blockTimeDiff)
			}
		}
	}
	lastBlockTimes.Set(stateType, parsedTime)

	// keep track of block heights for cleanup
	var blockHeights []int64
	blockHeightsIface, exists := blockHeightsByState.Get(stateType)
	if exists {
		blockHeights = blockHeightsIface.([]int64)
	}
	blockHeights = append(blockHeights, int64(height))

	// Cleanup old entries if we exceed maxBlockHistory
	if len(blockHeights) > maxBlockHistory {
		// Remove oldest entries
		blockHeights = blockHeights[len(blockHeights)-maxBlockHistory:]
	}
	blockHeightsByState.Set(stateType, blockHeights)
	lastBlockTimeMu.Unlock()

	// update metrics with state type label
	// only update block height from fast state to avoid conflicts
	if stateType == "fast" {
		metrics.SetBlockHeight(int64(height))
		metrics.SetLatestBlockTime(parsedTime.Unix())
	}

	// record apply duration with state type label
	metrics.RecordApplyDurationWithLabel(applyDurationMs, stateType)

	logger.DebugComponent("core", "Updated %s state metrics: height=%.0f, apply_duration=%.6f, block_time=%s UTC, begin_block_wall_time=%s",
		stateType, height, applyDuration, parsedTime.Format(time.RFC3339), beginBlockWallTime)

	return nil
}

func monitorLegacyBlockState(ctx context.Context, cfg config.Config, errCh chan<- error) {
	blockTimeDir := filepath.Join(cfg.NodeHome, "data", "block_times")
	var currentFile string
	var fileReader *bufio.Reader
	isFirstRun := true

	logger.InfoComponent("core", "Starting legacy block monitor for directory: %s", blockTimeDir)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// check for new files
			latestFile, err := utils.GetLatestFile(blockTimeDir)
			if err != nil {
				errCh <- fmt.Errorf("error finding latest block time file: %w", err)
				time.Sleep(1 * time.Second)
				continue
			}

			// if a new file is found, switch to it
			if latestFile != currentFile {
				logger.InfoComponent("core", "Switching to new block time file: %s", latestFile)
				if fileReader != nil {
					fileReader = nil // allow the old file to be garbage collected
				}
				file, err := os.Open(latestFile)
				if err != nil {
					errCh <- fmt.Errorf("error opening new block time file: %w", err)
					time.Sleep(1 * time.Second)
					continue
				}

				if isFirstRun {
					// on first run, seek to the end of the file
					_, err = file.Seek(0, io.SeekEnd)
					if err != nil {
						errCh <- fmt.Errorf("error seeking to end of file: %w", err)
						file.Close()
						time.Sleep(1 * time.Second)
						continue
					}
					logger.InfoComponent("core", "First run: starting to stream from the end of file %s", latestFile)
				} else {
					logger.InfoComponent("core", "Not first run: reading entire file %s", latestFile)
				}

				fileReader = bufio.NewReader(file)
				currentFile = latestFile
				isFirstRun = false
			}

			// read and process lines
			if fileReader != nil {
				for {
					line, err := fileReader.ReadString('\n')
					if err != nil {
						if err == io.EOF {
							// eof reached, wait a lil before checking for more data
							time.Sleep(10 * time.Millisecond)
							break
						}
						errCh <- fmt.Errorf("error reading from block time file: %w", err)
						break
					}
					// Skip empty lines
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					// process without state label for legacy format
					if err := parseLegacyBlockTimeLine(ctx, line); err != nil {
						// Skip invalid lines silently - these are likely partial writes
						// The next read cycle will get the complete line
						logger.DebugComponent("core", "Skipping potentially incomplete legacy block time line: %v", err)
					}
				}
			}
		}
	}
}

// for backward compatibility
func parseLegacyBlockTimeLine(ctx context.Context, line string) error {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error parsing block time line: %w", err)
	}

	height, ok := data["height"].(float64)
	if !ok {
		return fmt.Errorf("height not found or not a number")
	}

	blockTime, ok := data["block_time"].(string)
	if !ok {
		return fmt.Errorf("block time not found or not a string")
	}

	applyDuration, ok := data["apply_duration"].(float64)
	if !ok {
		return fmt.Errorf("apply duration not found or not a number")
	}

	// convert applyDuration from seconds to milliseconds
	applyDurationMs := applyDuration * 1000

	// parse block_time to Unix timestamp
	layout := "2006-01-02T15:04:05.999999999"
	parsedTime, err := time.Parse(layout, blockTime)
	if err != nil {
		return fmt.Errorf("error parsing block time: %w", err)
	}

	// assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()

	// calculate block time difference (use "legacy" as key)
	lastBlockTimeMu.Lock()
	lastTimeIface, exists := lastBlockTimes.Get("legacy")
	if exists {
		lastTime := lastTimeIface.(time.Time)
		if !lastTime.IsZero() {
			blockTimeDiff := parsedTime.Sub(lastTime).Milliseconds()
			if blockTimeDiff > 0 {
				metrics.RecordBlockTime(float64(blockTimeDiff))
				logger.DebugComponent("core", "Block time difference: %d milliseconds", blockTimeDiff)
			} else {
				logger.WarningComponent("core", "Invalid block time difference: %d milliseconds", blockTimeDiff)
			}
		}
	}
	lastBlockTimes.Set("legacy", parsedTime)

	// keep track of block heights for cleanup
	var blockHeights []int64
	blockHeightsIface, exists := blockHeightsByState.Get("legacy")
	if exists {
		blockHeights = blockHeightsIface.([]int64)
	}
	blockHeights = append(blockHeights, int64(height))

	// cleanup old entries if we exceed maxBlockHistory
	if len(blockHeights) > maxBlockHistory {
		// remove oldest entries
		blockHeights = blockHeights[len(blockHeights)-maxBlockHistory:]
	}
	blockHeightsByState.Set("legacy", blockHeights)
	lastBlockTimeMu.Unlock()

	// update metrics without labels (backward compatible)
	metrics.SetBlockHeight(int64(height))
	metrics.RecordApplyDuration(applyDurationMs)
	metrics.SetLatestBlockTime(parsedTime.Unix())

	logger.DebugComponent("core", "Updated metrics: height=%.0f, apply_duration=%.6f, block_time=%s UTC",
		height, applyDuration, parsedTime.Format(time.RFC3339))

	return nil
}
