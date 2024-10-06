package monitors

import (
	"bufio"
	"encoding/json"
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

// StartBlockMonitor starts monitoring block time logs
func StartBlockMonitor(cfg config.Config) {
	go func() {
		blockTimeDir := filepath.Join(cfg.NodeHome, "data/block_times")
		var currentFile string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			// Check for new files
			latestFile, err := utils.GetLatestFile(blockTimeDir)
			if err != nil {
				logger.Error("Error finding latest block time file: %v", err)
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
					logger.Error("Error opening new block time file: %v", err)
					time.Sleep(1 * time.Second)
					continue
				}

				if isFirstRun {
					// On first run, seek to the end of the file
					_, err = file.Seek(0, io.SeekEnd)
					if err != nil {
						logger.Error("Error seeking to end of file: %v", err)
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
					logger.Error("Error reading from block time file: %v", err)
					break
				}
				parseBlockTimeLine(line)
			}
		}
	}()
}

func processBlockTimeFile(filePath string, offset int64, isFirstRun bool) int64 {
	file, err := os.Open(filePath)
	if err != nil {
		logger.Error("Error opening block time file %s: %v", filePath, err)
		return offset
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		logger.Error("Error getting file info for %s: %v", filePath, err)
		return offset
	}

	if isFirstRun && offset == 0 {
		// If it's the first run and we haven't processed this file before,
		// start reading from the end of the file
		offset = fileInfo.Size()
	}

	_, err = file.Seek(offset, 0)
	if err != nil {
		logger.Error("Error seeking in file %s: %v", filePath, err)
		return offset
	}

	reader := bufio.NewReader(file)
	lineCount := 0
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error("Error reading line from block time file %s: %v", filePath, err)
			break
		}
		parseBlockTimeLine(line)
		lineCount++
		offset += int64(len(line))
	}

	logger.Info("Processed %d new lines from block time file %s", lineCount, filePath)
	return offset
}

func parseBlockTimeLine(line string) {
	var data map[string]interface{}
	err := json.Unmarshal([]byte(line), &data)
	if err != nil {
		logger.Error("Error parsing block time line: %v", err)
		return
	}

	height, ok := data["height"].(float64)
	if !ok {
		logger.Warning("Height not found or not a number")
		return
	}

	blockTime, ok := data["block_time"].(string)
	if !ok {
		logger.Warning("Block time not found or not a string")
		return
	}

	applyDuration, ok := data["apply_duration"].(float64)
	if !ok {
		logger.Warning("Apply duration not found or not a number")
		return
	}

	// Parse block_time to Unix timestamp
	layout := "2006-01-02T15:04:05.999999999"
	parsedTime, err := time.Parse(layout, blockTime)
	if err != nil {
		logger.Error("Error parsing block time: %v", err)
		return
	}

	// Assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()

	unixTimestamp := float64(parsedTime.Unix())

	// Calculate block time difference
	if !lastBlockTime.IsZero() {
		blockTimeDiff := parsedTime.Sub(lastBlockTime).Milliseconds()
		if blockTimeDiff > 0 {
			metrics.HLBlockTimeHistogram.Observe(float64(blockTimeDiff))
			logger.Debug("Block time difference: %d milliseconds", blockTimeDiff)
		} else {
			logger.Warning("Invalid block time difference: %d milliseconds", blockTimeDiff)
		}
	}

	// Update last block time
	lastBlockTime = parsedTime

	// Update metrics
	metrics.HLBlockHeightGauge.Set(height)
	metrics.HLApplyDurationGauge.Set(applyDuration)
	metrics.HLLatestBlockTimeGauge.Set(unixTimestamp)
	metrics.HLApplyDurationHistogram.Observe(applyDuration)

	logger.Debug("Updated metrics: height=%.0f, apply_duration=%.6f, block_time=%s UTC, unix_time=%f",
		height, applyDuration, parsedTime.Format(time.RFC3339), unixTimestamp)
}
