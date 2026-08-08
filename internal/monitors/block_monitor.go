package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

// track last block time separately for fast and slow states
var (
	lastBlockTimeMu  sync.RWMutex
	lastBlockTimes   = make(map[string]time.Time)
	lastBlockHeights = make(map[string]int64)
)

type blockLayout uint8

const (
	blockLayoutUnknown blockLayout = iota
	blockLayoutNone
	blockLayoutLegacy
	blockLayoutDual
)

func StartBlockMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceBlock, true)
	metrics.RegisterSource(metrics.SourceBlockFast, true)
	metrics.RegisterSource(metrics.SourceBlockSlow, true)
	metrics.RegisterSource(metrics.SourceBlockLegacy, true)

	var (
		current       blockLayout = blockLayoutUnknown
		cancelWorkers context.CancelFunc
		workers       sync.WaitGroup
	)
	stopWorkers := func() {
		if cancelWorkers == nil {
			return
		}
		cancelWorkers()
		workers.Wait()
		cancelWorkers = nil
	}
	startWorker := func(fn func()) {
		workers.Add(1)
		goSafe("block", func() {
			defer workers.Done()
			fn()
		})
	}
	reconcile := func() {
		metrics.MarkMonitorAttempt("block")
		layout, err := detectBlockLayout(cfg.NodeHome)
		if err != nil {
			metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureStat)
			ReportError(ctx, "block", errCh, fmt.Errorf("detect block-time layout: %w", err))
			return
		}
		if layout == blockLayoutNone {
			metrics.MarkSourceAbsent(metrics.SourceBlock)
			metrics.MarkSourceAbsent(metrics.SourceBlockFast)
			metrics.MarkSourceAbsent(metrics.SourceBlockSlow)
			metrics.MarkSourceAbsent(metrics.SourceBlockLegacy)
		}
		if layout == current {
			return
		}

		previous := current
		stopWorkers()
		current = layout
		consumeStartupRecords := previous != blockLayoutUnknown
		workerCtx, cancel := context.WithCancel(ctx)
		cancelWorkers = cancel
		switch layout {
		case blockLayoutDual:
			logger.InfoComponent("core", "Using dual fast/slow block-time layout")
			metrics.RegisterSource(metrics.SourceBlockFast, true)
			metrics.RegisterSource(metrics.SourceBlockSlow, true)
			metrics.RegisterSource(metrics.SourceBlockLegacy, false)
			startWorker(func() {
				monitorBlockState(workerCtx, cfg, "fast", "node_fast_block_times", errCh, consumeStartupRecords)
			})
			startWorker(func() {
				monitorBlockState(workerCtx, cfg, "slow", "node_slow_block_times", errCh, consumeStartupRecords)
			})
		case blockLayoutLegacy:
			logger.InfoComponent("core", "Using legacy block_times layout")
			metrics.RegisterSource(metrics.SourceBlockFast, false)
			metrics.RegisterSource(metrics.SourceBlockSlow, false)
			metrics.RegisterSource(metrics.SourceBlockLegacy, true)
			startWorker(func() { monitorLegacyBlockState(workerCtx, cfg, errCh, consumeStartupRecords) })
		case blockLayoutNone:
			cancelWorkers = nil
			cancel()
			logger.InfoComponent("core", "No block-time layout present; discovery remains active")
		}
	}

	reconcile()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer stopWorkers()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func detectBlockLayout(nodeHome string) (blockLayout, error) {
	dataRoot := filepath.Join(nodeHome, "data")
	exists := func(name string) (bool, error) {
		info, err := os.Stat(filepath.Join(dataRoot, name))
		if err == nil {
			if !info.IsDir() {
				return false, fmt.Errorf("%s is not a directory", name)
			}
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	fast, err := exists("node_fast_block_times")
	if err != nil {
		return blockLayoutUnknown, err
	}
	slow, err := exists("node_slow_block_times")
	if err != nil {
		return blockLayoutUnknown, err
	}
	legacy, err := exists("block_times")
	if err != nil {
		return blockLayoutUnknown, err
	}
	switch {
	case fast || slow:
		return blockLayoutDual, nil
	case legacy:
		return blockLayoutLegacy, nil
	default:
		return blockLayoutNone, nil
	}
}

func monitorBlockState(ctx context.Context, cfg config.Config, stateType string, dirName string, errCh chan<- error, consumeStartupRecords bool) {
	blockTimeDir := filepath.Join(cfg.NodeHome, "data", dirName)

	logger.InfoComponent("core", "Starting %s state block monitor for directory: %s", stateType, blockTimeDir)

	source := blockSourceID(stateType)

	tailStream(ctx, tailStreamOpts{
		component: "core",
		name:      stateType + " block times",
		resolve: func() (string, error) {
			metrics.MarkMonitorAttempt("block")
			metrics.MarkSourceAttempt(source)
			if _, err := os.Stat(blockTimeDir); err != nil {
				if os.IsNotExist(err) {
					metrics.MarkSourceAbsent(source)
				} else {
					metrics.MarkSourceError(source, metrics.SourceFailureStat)
					metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureStat)
					ReportError(ctx, "block", errCh, fmt.Errorf("stat %s block-time source: %w", stateType, err))
				}
				return "", err
			}
			path, err := utils.LatestFlatFile(blockTimeDir)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					metrics.MarkSourceAbsent(source)
				} else {
					metrics.MarkSourceError(source, metrics.SourceFailureDiscovery)
					metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureDiscovery)
					ReportError(ctx, "block", errCh, fmt.Errorf("discover %s block-time stream: %w", stateType, err))
				}
			}
			return path, err
		},
		rescanEvery:           2 * time.Second,
		eofSleep:              250 * time.Millisecond,
		consumeStartupRecords: consumeStartupRecords,
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("block")
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			if err := parseBlockTimeLine(line, stateType); err != nil {
				metrics.MarkSourceError(source, metrics.SourceFailureSchema)
				metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureSchema)
				ReportError(ctx, "block", errCh, fmt.Errorf("parse %s block-time record: %w", stateType, err))
				logger.DebugComponent("core", "Skipping unparseable %s block time line: %v", stateType, err)
			} else {
				metrics.MarkSourcePublication(source)
				metrics.MarkSourcePublication(metrics.SourceBlock)
			}
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(source, true)
			metrics.MarkSourceReadOutcome(metrics.SourceBlock, true)
		},
		onIdle: func() {
			metrics.MarkSourceReadOutcome(source, true)
			metrics.MarkSourceReadOutcome(metrics.SourceBlock, true)
		},
		onFailure: func(failure tailStreamFailure) {
			stage := blockTailFailureStage(failure)
			metrics.MarkSourceError(source, stage)
			metrics.MarkSourceError(metrics.SourceBlock, stage)
			ReportError(ctx, "block", errCh, fmt.Errorf("%s block-time stream %s failure", stateType, failure))
		},
	})
}

func parseBlockTimeLine(line string, stateType string) error {
	var data struct {
		Height             *int64   `json:"height"`
		BlockTime          string   `json:"block_time"`
		BeginBlockWallTime string   `json:"begin_block_wall_time"`
		ApplyDuration      *float64 `json:"apply_duration"`
	}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error parsing block time line: %w", err)
	}

	if data.Height == nil || *data.Height < 0 {
		return fmt.Errorf("height not found or not a number")
	}
	if data.BlockTime == "" {
		return fmt.Errorf("block time not found or not a string")
	}
	if data.ApplyDuration == nil || math.IsNaN(*data.ApplyDuration) || math.IsInf(*data.ApplyDuration, 0) || *data.ApplyDuration < 0 {
		return fmt.Errorf("apply duration not found or not a number")
	}

	// convert applyDuration from seconds to milliseconds
	applyDurationMs := *data.ApplyDuration * 1000
	if math.IsNaN(applyDurationMs) || math.IsInf(applyDurationMs, 0) {
		return fmt.Errorf("apply duration overflows milliseconds")
	}

	// parse block_time to Unix timestamp
	layout := "2006-01-02T15:04:05.999999999"
	parsedTime, err := time.Parse(layout, data.BlockTime)
	if err != nil {
		return fmt.Errorf("error parsing block time: %w", err)
	}

	// assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()
	var beginBlockWall time.Time
	if data.BeginBlockWallTime != "" {
		beginBlockWall, err = time.Parse(layout, data.BeginBlockWallTime)
		if err != nil {
			return fmt.Errorf("error parsing begin block wall time: %w", err)
		}
		beginBlockWall = beginBlockWall.UTC()
	}

	lastTime, exists, err := advanceBlockBaseline(stateType, *data.Height, parsedTime)
	if err != nil {
		return err
	}
	if exists {
		blockTimeDiff := blockIntervalMilliseconds(lastTime, parsedTime)
		metrics.RecordBlockTimeWithLabel(blockTimeDiff, stateType)
		logger.DebugComponent("core", "%s state block time difference: %.6f milliseconds", stateType, blockTimeDiff)
	}

	if !beginBlockWall.IsZero() {
		if lag, ok := beginWallReceiptLag(beginBlockWall, time.Now().UTC()); ok {
			metrics.HLCoreBlockBeginWallReceiptLag.WithLabelValues(stateType).Observe(lag)
		}
	}

	// update metrics with state type label
	// only update block height from fast state to avoid conflicts
	if stateType == "fast" {
		metrics.SetBlockHeight(*data.Height)
		metrics.SetLatestBlockTime(parsedTime.Unix())
	}

	// record apply duration with state type label
	metrics.RecordApplyDurationWithLabel(applyDurationMs, stateType)
	metrics.MarkMonitorValidObservation("block")
	metrics.MarkMonitorPublication("block")
	metrics.MarkSourceValidObservation(blockSourceID(stateType), parsedTime)
	metrics.MarkSourceValidObservation(metrics.SourceBlock, parsedTime)

	logger.DebugComponent("core", "Updated %s state metrics: height=%d, apply_duration=%.6f, block_time=%s UTC, begin_block_wall_time=%s",
		stateType, *data.Height, *data.ApplyDuration, parsedTime.Format(time.RFC3339), data.BeginBlockWallTime)

	return nil
}

func blockSourceID(stateType string) metrics.SourceID {
	switch stateType {
	case "fast":
		return metrics.SourceBlockFast
	case "slow":
		return metrics.SourceBlockSlow
	default:
		return metrics.SourceBlockLegacy
	}
}

func beginWallReceiptLag(begin, receipt time.Time) (float64, bool) {
	if begin.IsZero() || receipt.IsZero() || begin.After(receipt) {
		return 0, false
	}
	return receipt.Sub(begin).Seconds(), true
}

func blockIntervalMilliseconds(previous, current time.Time) float64 {
	return current.Sub(previous).Seconds() * 1000
}

func advanceBlockBaseline(stateType string, height int64, parsedTime time.Time) (time.Time, bool, error) {
	lastBlockTimeMu.Lock()
	defer lastBlockTimeMu.Unlock()
	lastTime, hasTime := lastBlockTimes[stateType]
	lastHeight, hasHeight := lastBlockHeights[stateType]
	if hasTime && !parsedTime.After(lastTime) {
		return time.Time{}, false, fmt.Errorf("%s block time did not advance", stateType)
	}
	if hasHeight && height <= lastHeight {
		return time.Time{}, false, fmt.Errorf("%s block height did not advance", stateType)
	}
	lastBlockTimes[stateType] = parsedTime
	lastBlockHeights[stateType] = height
	return lastTime, hasTime, nil
}

func blockTailFailureStage(failure tailStreamFailure) metrics.SourceFailureStage {
	switch failure {
	case tailStreamFailureOpen:
		return metrics.SourceFailureOpen
	case tailStreamFailureStat:
		return metrics.SourceFailureStat
	case tailStreamFailureRecord:
		return metrics.SourceFailureSchema
	default:
		return metrics.SourceFailureRead
	}
}

func monitorLegacyBlockState(ctx context.Context, cfg config.Config, errCh chan<- error, consumeStartupRecords bool) {
	blockTimeDir := filepath.Join(cfg.NodeHome, "data", "block_times")

	logger.InfoComponent("core", "Starting legacy block monitor for directory: %s", blockTimeDir)

	tailStream(ctx, tailStreamOpts{
		component: "core",
		name:      "legacy block times",
		// old releases used either hourly/<date>/<h> nesting or flat date
		// files depending on version; try nested first
		resolve: func() (string, error) {
			metrics.MarkMonitorAttempt("block")
			metrics.MarkSourceAttempt(metrics.SourceBlockLegacy)
			if _, err := os.Stat(blockTimeDir); err != nil {
				if os.IsNotExist(err) {
					metrics.MarkSourceAbsent(metrics.SourceBlockLegacy)
				} else {
					metrics.MarkSourceError(metrics.SourceBlockLegacy, metrics.SourceFailureStat)
					metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureStat)
					ReportError(ctx, "block", errCh, fmt.Errorf("stat legacy block-time source: %w", err))
				}
				return "", err
			}
			if p, err := latestHourlyFile(blockTimeDir); err == nil {
				return p, nil
			}
			path, err := utils.LatestFlatFile(blockTimeDir)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					metrics.MarkSourceAbsent(metrics.SourceBlockLegacy)
				} else {
					metrics.MarkSourceError(metrics.SourceBlockLegacy, metrics.SourceFailureDiscovery)
					metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureDiscovery)
					ReportError(ctx, "block", errCh, fmt.Errorf("discover legacy block-time stream: %w", err))
				}
			}
			return path, err
		},
		rescanEvery:           2 * time.Second,
		eofSleep:              250 * time.Millisecond,
		consumeStartupRecords: consumeStartupRecords,
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("block")
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			if err := parseLegacyBlockTimeLine(line); err != nil {
				metrics.MarkSourceError(metrics.SourceBlockLegacy, metrics.SourceFailureSchema)
				metrics.MarkSourceError(metrics.SourceBlock, metrics.SourceFailureSchema)
				ReportError(ctx, "block", errCh, fmt.Errorf("parse legacy block-time record: %w", err))
				logger.DebugComponent("core", "Skipping unparseable legacy block time line: %v", err)
			} else {
				metrics.MarkSourcePublication(metrics.SourceBlockLegacy)
				metrics.MarkSourcePublication(metrics.SourceBlock)
			}
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(metrics.SourceBlockLegacy, true)
			metrics.MarkSourceReadOutcome(metrics.SourceBlock, true)
		},
		onIdle: func() {
			metrics.MarkSourceReadOutcome(metrics.SourceBlockLegacy, true)
			metrics.MarkSourceReadOutcome(metrics.SourceBlock, true)
		},
		onFailure: func(failure tailStreamFailure) {
			stage := blockTailFailureStage(failure)
			metrics.MarkSourceError(metrics.SourceBlockLegacy, stage)
			metrics.MarkSourceError(metrics.SourceBlock, stage)
			ReportError(ctx, "block", errCh, fmt.Errorf("legacy block-time stream %s failure", failure))
		},
	})
}

// for backward compatibility
func parseLegacyBlockTimeLine(line string) error {
	var data struct {
		Height        *int64   `json:"height"`
		BlockTime     string   `json:"block_time"`
		ApplyDuration *float64 `json:"apply_duration"`
	}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return fmt.Errorf("error parsing block time line: %w", err)
	}
	if data.Height == nil || *data.Height < 0 {
		return fmt.Errorf("height not found or not a number")
	}
	if data.BlockTime == "" {
		return fmt.Errorf("block time not found or not a string")
	}
	if data.ApplyDuration == nil || math.IsNaN(*data.ApplyDuration) || math.IsInf(*data.ApplyDuration, 0) || *data.ApplyDuration < 0 {
		return fmt.Errorf("apply duration not found or not a number")
	}

	// convert applyDuration from seconds to milliseconds
	applyDurationMs := *data.ApplyDuration * 1000
	if math.IsNaN(applyDurationMs) || math.IsInf(applyDurationMs, 0) {
		return fmt.Errorf("apply duration overflows milliseconds")
	}

	// parse block_time to Unix timestamp
	layout := "2006-01-02T15:04:05.999999999"
	parsedTime, err := time.Parse(layout, data.BlockTime)
	if err != nil {
		return fmt.Errorf("error parsing block time: %w", err)
	}

	// assume the time is in UTC if no timezone is specified
	parsedTime = parsedTime.UTC()

	lastTime, exists, err := advanceBlockBaseline("legacy", *data.Height, parsedTime)
	if err != nil {
		return err
	}
	if exists {
		blockTimeDiff := blockIntervalMilliseconds(lastTime, parsedTime)
		metrics.RecordBlockTime(blockTimeDiff)
		logger.DebugComponent("core", "Block time difference: %.6f milliseconds", blockTimeDiff)
	}

	// update metrics without labels (backward compatible)
	metrics.SetBlockHeight(*data.Height)
	metrics.RecordApplyDuration(applyDurationMs)
	metrics.SetLatestBlockTime(parsedTime.Unix())
	metrics.MarkMonitorValidObservation("block")
	metrics.MarkMonitorPublication("block")
	metrics.MarkSourceValidObservation(metrics.SourceBlockLegacy, parsedTime)
	metrics.MarkSourceValidObservation(metrics.SourceBlock, parsedTime)

	logger.DebugComponent("core", "Updated metrics: height=%d, apply_duration=%.6f, block_time=%s UTC",
		*data.Height, *data.ApplyDuration, parsedTime.Format(time.RFC3339))

	return nil
}
