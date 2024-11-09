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

func StartEVMBlockHeightMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// Wait a bit for validator status to be determined
	time.Sleep(60 * time.Second)

	// Only proceed if we're a validator
	if metrics.IsValidator() {
		logger.Info("Node is a validator, skipping EVM monitoring")
		return
	}

	evmBlockHeightDir := filepath.Join(cfg.NodeHome, "data/dhs/EthBlocks/hourly")
	logger.Info("Starting EVM monitoring for validator node in directory: %s", evmBlockHeightDir)

	go func() {
		if _, err := os.Stat(evmBlockHeightDir); os.IsNotExist(err) {
			logger.Warning("EVM block height directory does not exist: %s", evmBlockHeightDir)
			// Continue running but log info - directory might be created later
		}

		var currentBlockHeightFilePath string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(1 * time.Second)

				latestFile, err := utils.GetLatestFile(evmBlockHeightDir)
				if err != nil {
					errCh <- fmt.Errorf("error finding latest EVM block height file: %w", err)
					continue
				}

				if latestFile != currentBlockHeightFilePath {
					logger.Info("Switching to new EVM block height file: %s", latestFile)

					if fileReader != nil {
						fileReader = nil
					}

					file, err := os.Open(latestFile)
					if err != nil {
						errCh <- fmt.Errorf("error opening EVM block height file: %w", err)
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
					currentBlockHeightFilePath = latestFile
				}

				if fileReader != nil {
					for {
						line, err := fileReader.ReadString('\n')
						if err != nil {
							if err == io.EOF {
								break
							}
							errCh <- fmt.Errorf("error reading from EVM block height file: %w", err)
							break
						}

						if err := processEVMBlockHeightLine(line); err != nil {
							errCh <- fmt.Errorf("error processing EVM block height line: %w", err)
							continue
						}
					}
				}
			}
		}
	}()
}

func processEVMBlockHeightLine(line string) error {
	var blockData []interface{}
	if err := json.Unmarshal([]byte(line), &blockData); err != nil {
		return fmt.Errorf("error unmarshaling EVM block height data: %w", err)
	}
	if len(blockData) < 2 {
		return fmt.Errorf("invalid block data format: expected at least 2 elements, got %d", len(blockData))
	}

	blockNumberFloat, ok := blockData[1].(float64)
	if !ok {
		return fmt.Errorf("invalid block number format: expected number, got %T", blockData[1])
	}
	blockNumber := int64(blockNumberFloat)

	metrics.SetEVMBlockHeight(blockNumber)

	return nil
}
