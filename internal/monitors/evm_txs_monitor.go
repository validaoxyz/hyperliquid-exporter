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

func StartEVMTransactionsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// Wait a bit for validator status to be determined
	time.Sleep(60 * time.Second)

	// Only proceed if we're a validator
	if metrics.IsValidator() {
		logger.Info("Node is a validator, skipping EVM transactions monitoring")
		return
	}

	evmTxsDir := filepath.Join(cfg.NodeHome, "data/dhs/EthTxs/hourly")
	logger.Info("Starting EVM transactions monitoring for validator node in directory: %s", evmTxsDir)

	go func() {
		if _, err := os.Stat(evmTxsDir); os.IsNotExist(err) {
			logger.Warning("EVM transactions directory does not exist: %s", evmTxsDir)
			// Continue running but log info - directory might be created later
		}

		var currentTxsFilePath string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(1 * time.Second)

				latestFile, err := utils.GetLatestFile(evmTxsDir)
				if err != nil {
					errCh <- fmt.Errorf("error finding latest EVM transactions file: %w", err)
					continue
				}

				if latestFile != currentTxsFilePath {
					logger.Info("Switching to new EVM transactions file: %s", latestFile)

					if fileReader != nil {
						fileReader = nil
					}

					file, err := os.Open(latestFile)
					if err != nil {
						errCh <- fmt.Errorf("error opening EVM transactions file: %w", err)
						continue
					}

					if isFirstRun {
						_, err = file.Seek(0, io.SeekEnd)
						if err != nil {
							errCh <- fmt.Errorf("error seeking to end of file: %w", err)
							file.Close()
							continue
						}
						logger.Info("First run: starting to stream from the end of file %s", latestFile)
						isFirstRun = false
					} else {
						logger.Info("Not first run: reading entire file %s", latestFile)
					}

					fileReader = bufio.NewReader(file)
					currentTxsFilePath = latestFile
				}

				if fileReader != nil {
					for {
						line, err := fileReader.ReadString('\n')
						if err != nil {
							if err == io.EOF {
								break
							}
							errCh <- fmt.Errorf("error reading from EVM transactions file: %w", err)
							break
						}

						if err := processEVMTransactionLine(line); err != nil {
							errCh <- fmt.Errorf("error processing EVM transaction line: %w", err)
							continue
						}
					}
				}
			}
		}
	}()
}

func processEVMTransactionLine(line string) error {
	var txData []interface{}
	if err := json.Unmarshal([]byte(line), &txData); err != nil {
		return fmt.Errorf("error unmarshaling EVM transaction data: %w", err)
	}

	metrics.IncrementEVMTransactionsCounter()

	return nil
}
