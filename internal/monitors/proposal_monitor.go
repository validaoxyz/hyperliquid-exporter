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

func StartProposalMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	go func() {
		// skip if replica monitoring is enabled as it will handle proposer counting
		if cfg.EnableReplicaMetrics {
			// already logged in exporter.go, just return silently
			return
		}

		// wait a short time at startup to ensure signer mappings are populated
		// prevents early proposals from having incorrect validator labels
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			logger.DebugComponent("consensus", "Initial startup delay complete, beginning proposal monitoring")
		}

		logger.InfoComponent("consensus", "Proposal monitor started - tracking block proposers")

		logsDir := filepath.Join(cfg.NodeHome, "data/replica_cmds")
		var currentFile string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// check for new files
				latestFile, err := utils.GetLatestFile(logsDir)
				if err != nil {
					errCh <- fmt.Errorf("error finding latest proposal log file: %w", err)
					time.Sleep(1 * time.Second)
					continue
				}

				// if a new file is found, switch to it
				if latestFile != currentFile {
					logger.InfoComponent("consensus", "Switching to new proposal log file: %s", latestFile)
					if fileReader != nil {
						fileReader = nil // allow the old file to be garbage collected
					}
					file, err := os.Open(latestFile)
					if err != nil {
						errCh <- fmt.Errorf("error opening new proposal log file: %w", err)
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
						logger.InfoComponent("consensus", "First run: starting to stream from the end of file %s", latestFile)
					} else {
						logger.InfoComponent("consensus", "Not first run: reading entire file %s", latestFile)
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
	// quick sanity-skip: logs sometimes emit plain-text lines; ignore if line doesn't start with '[' or '{'
	if len(line) == 0 || (line[0] != '[' && line[0] != '{') {
		return nil
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		// skip malformed JSON lines silently
		return nil
	}

	abciBlock, ok := data["abci_block"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("ABCI block not found in proposal line")
	}

	proposer, ok := abciBlock["proposer"].(string)
	if !ok {
		return fmt.Errorf("proposer not found in ABCI block")
	}

	// update OpenTelemetry metric
	metrics.IncrementProposerCounter(proposer)

	logger.DebugComponent("consensus", "Proposer %s counter incremented", proposer)
	return nil
}
