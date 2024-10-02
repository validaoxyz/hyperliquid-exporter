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

// StartBlockMonitor starts monitoring block time logs
func StartBlockMonitor(cfg config.Config) {
	go func() {
		blockTimeDir := filepath.Join(cfg.NodeHome, "hl/data/block_times")
		var latestFile string
		var fileOffset int64 = 0
		for {
			newLatestFile, err := utils.GetLatestFile(blockTimeDir)
			if err != nil {
				logger.Error("Error finding latest block time file: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if newLatestFile != latestFile {
				logger.Info("Switching to new block time file: %s", newLatestFile)
				latestFile = newLatestFile
				fileOffset = 0
			}

			fileOffset = processBlockTimeFile(latestFile, fileOffset)
			time.Sleep(3 * time.Second)
		}
	}()
}

func processBlockTimeFile(filePath string, offset int64) int64 {
	file, err := os.Open(filePath)
	if err != nil {
		logger.Error("Error opening block time file %s: %v", filePath, err)
		return offset
	}
	defer file.Close()

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
	layout := "2006-01-02T15:04:05.999"
	parsedTime, err := time.Parse(layout, blockTime)
	if err != nil {
		logger.Error("Error parsing block time: %v", err)
		return
	}

	// Assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()

	unixTimestamp := float64(parsedTime.Unix())

	// Update metrics
	metrics.HLBlockHeightGauge.Set(height)
	metrics.HLApplyDurationGauge.Set(applyDuration)
	metrics.HLLatestBlockTimeGauge.Set(unixTimestamp)
	metrics.HLApplyDurationHistogram.Observe(applyDuration)

	logger.Debug("Updated metrics: height=%.0f, apply_duration=%.6f, block_time=%s UTC, unix_time=%f",
		height, applyDuration, parsedTime.Format(time.RFC3339), unixTimestamp)
}
