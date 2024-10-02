package monitors

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
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
				log.Printf("Error finding latest block time file: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if newLatestFile != latestFile {
				log.Printf("Switching to new block time file: %s", newLatestFile)
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
		log.Printf("Error opening block time file %s: %v", filePath, err)
		return offset
	}
	defer file.Close()

	_, err = file.Seek(offset, 0)
	if err != nil {
		log.Printf("Error seeking in file %s: %v", filePath, err)
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
			log.Printf("Error reading line from block time file %s: %v", filePath, err)
			break
		}
		parseBlockTimeLine(line)
		lineCount++
		offset += int64(len(line))
	}

	log.Printf("Processed %d new lines from block time file %s", lineCount, filePath)
	return offset
}

func parseBlockTimeLine(line string) {
	var data map[string]interface{}
	err := json.Unmarshal([]byte(line), &data)
	if err != nil {
		log.Printf("Error parsing block time line: %v", err)
		return
	}

	// Variables to hold the parsed values
	var height float64
	var applyDuration float64

	// Extract and parse 'height'
	if heightValue, ok := data["height"]; ok {
		switch v := heightValue.(type) {
		case float64:
			height = v
		case string:
			height, err = strconv.ParseFloat(v, 64)
			if err != nil {
				log.Printf("Error parsing height value '%v' to float64: %v", v, err)
				return
			}
		default:
			log.Printf("Unexpected type for 'height': %T", v)
			return
		}
	} else {
		log.Printf("'height' key not found in data")
		return
	}

	// Extract and parse 'apply_duration'
	if applyDurationValue, ok := data["apply_duration"]; ok {
		switch v := applyDurationValue.(type) {
		case float64:
			applyDuration = v
		case string:
			applyDuration, err = strconv.ParseFloat(v, 64)
			if err != nil {
				log.Printf("Error parsing apply_duration value '%v' to float64: %v", v, err)
				return
			}
		default:
			log.Printf("Unexpected type for 'apply_duration': %T", v)
			return
		}
	} else {
		log.Printf("'apply_duration' key not found in data")
		return
	}

	// Update metrics
	metrics.HLBlockHeightGauge.Set(height)
	metrics.HLApplyDurationGauge.Set(applyDuration)

	log.Printf("Updated metrics: height=%.0f, apply_duration=%.6f", height, applyDuration)
}
