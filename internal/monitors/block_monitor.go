package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

var lastBlockTime time.Time

func StartBlockMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		blockTimeDir := filepath.Join(cfg.NodeHome, "data/node_fast_block_times")
		var currentFile string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Check for new files
				latestFile, err := utils.GetLatestFile(blockTimeDir)
				if err != nil {
					errCh <- fmt.Errorf("error finding latest block time file: %w", err)
					time.Sleep(1 * time.Second)
					continue
				}

				// If a new file is found, switch to it
				if latestFile != currentFile {
					logger.Info("Switching to new block time file: %s", latestFile)
					if fileReader != nil {
						fileReader = nil // Allow the old file to be garbage collected
					}
					file, err := os.Open(latestFile)
					if err != nil {
						errCh <- fmt.Errorf("error opening new block time file: %w", err)
						time.Sleep(1 * time.Second)
						continue
					}

					if isFirstRun {
						// On first run, seek to the end of the file
						_, err = file.Seek(0, io.SeekEnd)
						if err != nil {
							errCh <- fmt.Errorf("error seeking to end of file: %w", err)
							file.Close()
							time.Sleep(1 * time.Second)
							continue
						}
						logger.Info("First run: starting to stream from the end of file %s", latestFile)
					} else {
						logger.Info("Not first run: reading entire file %s", latestFile)
					}

					fileReader = bufio.NewReader(file)
					currentFile = latestFile
					isFirstRun = false
				}

				// Read and process lines
				for {
					line, err := fileReader.ReadString('\n')
					if err != nil {
						if err == io.EOF {
							// End of file reached, wait a bit before checking for more data
							time.Sleep(100 * time.Millisecond)
							break
						}
						errCh <- fmt.Errorf("error reading from block time file: %w", err)
						break
					}
					if err := parseBlockTimeLine(ctx, line); err != nil {
						errCh <- fmt.Errorf("error parsing block time line: %w", err)
					}
				}
			}
		}
	}()
}

func parseBlockTimeLine(ctx context.Context, line string) error {
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

	// Convert applyDuration from seconds to milliseconds
	applyDurationMs := applyDuration * 1000

	// Parse block_time to Unix timestamp
	layout := "2006-01-02T15:04:05.999999999"
	parsedTime, err := time.Parse(layout, blockTime)
	if err != nil {
		return fmt.Errorf("error parsing block time: %w", err)
	}

	// Assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()

	// Calculate block time difference
	if !lastBlockTime.IsZero() {
		blockTimeDiff := parsedTime.Sub(lastBlockTime).Milliseconds()
		if blockTimeDiff > 0 {
			metrics.RecordBlockTime(float64(blockTimeDiff))
			logger.Debug("Block time difference: %d milliseconds", blockTimeDiff)
		} else {
			logger.Warning("Invalid block time difference: %d milliseconds", blockTimeDiff)
		}
	}

	// Update last block time
	lastBlockTime = parsedTime

	// Update metrics
	metrics.SetBlockHeight(int64(height))
	metrics.RecordApplyDuration(applyDurationMs)
	metrics.SetLatestBlockTime(parsedTime.Unix())

	logger.Debug("Updated metrics: height=%.0f, apply_duration=%.6f, block_time=%s UTC",
		height, applyDuration, parsedTime.Format(time.RFC3339))

	return nil
}
