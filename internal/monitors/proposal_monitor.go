package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

var (
	proposerCounts = make(map[string]int)
	proposerMutex  sync.Mutex
)

func StartProposalMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		logsDir := filepath.Join(cfg.NodeHome, "data/replica_cmds")
		var currentFile string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Check for new files
				latestFile, err := utils.GetLatestFile(logsDir)
				if err != nil {
					errCh <- fmt.Errorf("error finding latest proposal log file: %w", err)
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
						errCh <- fmt.Errorf("error opening new proposal log file: %w", err)
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
						errCh <- fmt.Errorf("error reading from proposal log file: %w", err)
						break
					}
					if err := parseProposalLine(ctx, line); err != nil {
						errCh <- fmt.Errorf("error parsing proposal line: %w", err)
					}
				}
			}
		}
	}()
}

func parseProposalLine(ctx context.Context, line string) error {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error parsing proposal line: %w", err)
	}

	abciBlock, ok := data["abci_block"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("ABCI block not found in proposal line")
	}

	proposer, ok := abciBlock["proposer"].(string)
	if !ok {
		return fmt.Errorf("proposer not found in ABCI block")
	}

	// Update OpenTelemetry metric
	metrics.IncrementProposerCounter(proposer)

	// Update our local counter
	proposerMutex.Lock()
	proposerCounts[proposer]++
	count := proposerCounts[proposer]
	proposerMutex.Unlock()

	logger.Debug("Proposer %s counter incremented. Local count: %d", proposer, count)
	return nil
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
