package monitors

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

// StartProposalMonitor starts monitoring proposal logs
func StartProposalMonitor(cfg config.Config) {
	go func() {
		logsDir := filepath.Join(cfg.NodeHome, "data/replica_cmds")
		var currentFile string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			// Check for new files
			latestFile, err := utils.GetLatestFile(logsDir)
			if err != nil {
				logger.Error("Error finding latest proposal log file: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}

			// If a new file is found, switch to it
			if latestFile != currentFile {
				logger.Info("Switching to new proposal log file: %s", latestFile)
				if fileReader != nil {
					fileReader = nil // Allow the old file to be garbage collected
				}
				file, err := os.Open(latestFile)
				if err != nil {
					logger.Error("Error opening new proposal log file: %v", err)
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
					logger.Error("Error reading from proposal log file: %v", err)
					break
				}
				parseProposalLine(line)
			}
		}
	}()
}

func processProposalFile(filePath string, offset int64) int64 {
	file, err := os.Open(filePath)
	if err != nil {
		logger.Error("Error opening proposal file %s: %v", filePath, err)
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
			logger.Error("Error reading line from proposal file %s: %v", filePath, err)
			break
		}
		parseProposalLine(line)
		lineCount++
		offset += int64(len(line))
	}

	logger.Info("Processed %d new lines from proposal file %s", lineCount, filePath)
	return offset
}

var (
	proposerCounts = make(map[string]int)
	proposerMutex  sync.Mutex
)

func parseProposalLine(line string) {
	var data map[string]interface{}
	err := json.Unmarshal([]byte(line), &data)
	if err != nil {
		logger.Error("Error parsing proposal line: %v", err)
		return
	}

	abciBlock, ok := data["abci_block"].(map[string]interface{})
	if !ok {
		logger.Error("ABCI block not found in proposal line")
		return
	}

	proposer, ok := abciBlock["proposer"].(string)
	if !ok {
		logger.Error("Proposer not found in ABCI block")
		return
	}

	metrics.HLProposerCounter.WithLabelValues(proposer).Inc()

	// Update our local counter
	proposerMutex.Lock()
	proposerCounts[proposer]++
	count := proposerCounts[proposer]
	proposerMutex.Unlock()

	logger.Debug("Proposer %s counter incremented. Local count: %d", proposer, count)
}

func GetProposerCounts() map[string]int {
	proposerMutex.Lock()
	defer proposerMutex.Unlock()
	counts := make(map[string]int)
	for k, v := range proposerCounts {
		counts[k] = v
	}
	return counts
}
