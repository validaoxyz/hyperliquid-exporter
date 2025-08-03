package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

// starts monitoring node status logs and counts timeout rounds per suspect validator
func StartRoundAdvanceMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// only run this monitor on validator nodes
	if !metrics.IsValidator() {
		logger.InfoComponent("consensus", "Skipping round advance monitor - not a validator node")
		return
	}

	// logs live under <node_home>/data/node_logs/status/hourly/YYYYMMDD/<hour>
	statusHourlyRoot := filepath.Join(cfg.NodeHome, "data/node_logs/status/hourly")

	go func() {
		var currentFile string
		var fileReader *bufio.Reader
		isFirstRun := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// ensure directory exists; if not, sleep and retry without spamming error channel
				if _, statErr := os.Stat(statusHourlyRoot); errors.Is(statErr, os.ErrNotExist) {
					// directory not created yet – silent retry
					time.Sleep(30 * time.Second)
					continue
				}

				// find most-recent file anywhere under statusHourlyRoot
				latestFile, err := utils.GetLatestFile(statusHourlyRoot)
				if err != nil {
					// if underlying error is ENOENT it means dir exists but empty – treat same as no file
					if errors.Is(err, os.ErrNotExist) {
						time.Sleep(10 * time.Second)
						continue
					}
					errCh <- fmt.Errorf("error finding latest round_advance log file: %w", err)
					time.Sleep(5 * time.Second)
					continue
				}

				if latestFile == "" {
					// directory empty, retry later
					time.Sleep(10 * time.Second)
					continue
				}

				if latestFile != currentFile {
					if fileReader != nil {
						fileReader = nil // cleanup
					}
					file, err := os.Open(latestFile)
					if err != nil {
						errCh <- fmt.Errorf("error opening round_advance log file: %w", err)
						time.Sleep(1 * time.Second)
						continue
					}

					if isFirstRun {
						_, err = file.Seek(0, io.SeekEnd)
						if err != nil {
							errCh <- fmt.Errorf("error seeking to end of file: %w", err)
							file.Close()
							time.Sleep(1 * time.Second)
							continue
						}
						logger.InfoComponent("consensus", "First run: starting to stream round_advance from the end of file %s", latestFile)
						isFirstRun = false
					} else {
						logger.InfoComponent("consensus", "Not first run: reading entire round_advance file %s", latestFile)
					}

					fileReader = bufio.NewReader(file)
					currentFile = latestFile
				}

				if fileReader != nil {
					for {
						line, err := fileReader.ReadString('\n')
						if err != nil {
							if err == io.EOF {
								time.Sleep(10 * time.Millisecond)
								break
							}
							errCh <- fmt.Errorf("error reading round_advance log: %w", err)
							break
						}
						if err := parseRoundAdvanceLine(line); err != nil {
							// non-fatal parsing error – log at debug level
							logger.DebugComponent("consensus", "round_advance parse skip: %v", err)
						}
					}
				}
			}
		}
	}()
}

func parseRoundAdvanceLine(line string) error {
	var outer []interface{}
	if err := json.Unmarshal([]byte(line), &outer); err != nil {
		return fmt.Errorf("json unmarshal: %w", err)
	}
	if len(outer) < 2 {
		return fmt.Errorf("unexpected log shape")
	}

	// first element should be the event name
	evtName, ok := outer[0].(string)
	if !ok || evtName != "round_advance" {
		return nil // not a round_advance line – silently ignore
	}

	payload, ok := outer[1].(map[string]interface{})
	if !ok {
		return fmt.Errorf("payload not object")
	}

	reason, _ := payload["reason"].(string)
	if reason != "timeout" {
		return nil // we only care about timeouts
	}

	suspect, _ := payload["suspect"].(string)
	if suspect == "" {
		suspect = "unknown"
	}

	metrics.IncrementTimeoutRounds(suspect)
	return nil
}
