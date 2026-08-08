// Package monitors provides monitoring implementations for Hyperliquid node
// data sources.
package monitors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	streamevm "github.com/validaoxyz/hyperliquid-exporter/internal/evm"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const defaultRecipientMetricLimit = 20

// StartEVMMonitor follows committed records from evm_block_and_receipts. The
// stream is serial, but the processor itself is synchronized so test probes or
// future callers cannot race its interval/address state.
func StartEVMMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceEVM, true)
	processor := newEVMProcessor(productionEVMSink{}, cfg.EnableContractMetrics, cfg.ContractMetricsLimit)

	logger.InfoComponent(
		"evm",
		"EVM monitor config: recipientDiagnostics=%v, recipientLimit=%d",
		cfg.EnableContractMetrics,
		processor.recipients.limit,
	)
	// Preserve the historical validator-status grace period without making
	// shutdown wait for an unconditional sleep.
	if !sleepCtx(ctx, 60*time.Second) {
		return
	}
	if metrics.IsValidator() {
		logger.WarningComponent("evm", "Running EVM monitoring on a validator node - this may impact performance")
	}

	evmDataDir := filepath.Join(cfg.NodeHome, "data/evm_block_and_receipts/hourly")
	logger.InfoComponent("evm", "Starting EVM monitoring in directory: %s", evmDataDir)

	tailStream(ctx, tailStreamOpts{
		component:   "evm",
		name:        "evm block+receipts",
		rescanEvery: 2 * time.Second,
		eofSleep:    250 * time.Millisecond,
		resolve: func() (string, error) {
			metrics.MarkMonitorAttempt("evm")
			metrics.MarkSourceAttempt(metrics.SourceEVM)
			path, err := latestHourlyFile(evmDataDir)
			if err == nil {
				return path, nil
			}
			if errors.Is(err, os.ErrNotExist) {
				metrics.MarkSourceAbsent(metrics.SourceEVM)
			} else {
				metrics.MarkSourceError(metrics.SourceEVM, metrics.SourceFailureDiscovery)
			}
			return "", err
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(metrics.SourceEVM, true)
		},
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("evm")
			metrics.MarkSourceAttempt(metrics.SourceEVM)
			if err := processor.processLine(line); err != nil {
				recordEVMParseFailure(err)
				ReportError(ctx, "evm", errCh, fmt.Errorf("process EVM stream record: %w", err))
				return
			}
			metrics.MarkSourceValidObservation(metrics.SourceEVM, processor.lastSourceTime())
			metrics.MarkSourcePublication(metrics.SourceEVM)
			metrics.MarkMonitorValidObservation("evm")
			metrics.MarkMonitorPublication("evm")
		},
		onFailure: func(failure tailStreamFailure) {
			switch failure {
			case tailStreamFailureOpen:
				metrics.MarkSourceError(metrics.SourceEVM, metrics.SourceFailureOpen)
			case tailStreamFailureStat:
				metrics.MarkSourceError(metrics.SourceEVM, metrics.SourceFailureStat)
			case tailStreamFailureSeek, tailStreamFailureRead:
				metrics.MarkSourceError(metrics.SourceEVM, metrics.SourceFailureRead)
			case tailStreamFailureRecord:
				metrics.MarkSourceError(metrics.SourceEVM, metrics.SourceFailureSchema)
			}
		},
	})
}

func recordEVMParseFailure(err error) {
	var parseErr *streamevm.ParseError
	if errors.As(err, &parseErr) {
		metrics.IncrementEVMParseError(string(parseErr.Stage), string(parseErr.Reason))
		stage := metrics.SourceFailureSchema
		if parseErr.Reason == streamevm.ReasonInvalidJSON {
			stage = metrics.SourceFailureDecode
		}
		metrics.MarkSourceError(metrics.SourceEVM, stage)
		return
	}
	metrics.IncrementEVMParseError("other", "other")
	metrics.MarkSourceError(metrics.SourceEVM, metrics.SourceFailureSchema)
}

type evmMetricSink interface {
	setBlockHeight(int64)
	recordBlockTimeMilliseconds(float64)
	setLatestBlockTime(int64)
	setGasSnapshot(float64, float64, *float64)
	setBaseFeeGwei(float64)
	setPriorityFeeGwei(float64, bool)
	recordTxCount(int)
	incrementTxType(string)
	incrementTxShape(string, uint64)
	incrementRecipient(string)
	incrementReceiptOutcome(string, uint64)
	markCountMismatch(int64)
	addSystemTxItems(int)
	addPrecompileCalls(string, uint64)
}

type productionEVMSink struct{}

func (productionEVMSink) setBlockHeight(height int64) {
	metrics.SetEVMBlockHeight(height)
}

func (productionEVMSink) recordBlockTimeMilliseconds(milliseconds float64) {
	metrics.RecordEVMBlockTime(milliseconds)
}

func (productionEVMSink) setLatestBlockTime(timestamp int64) {
	metrics.SetEVMLatestBlockTime(timestamp)
}

func (productionEVMSink) setGasSnapshot(used, limit float64, ratio *float64) {
	metrics.SetEVMGasSnapshot(used, limit, ratio)
}

func (productionEVMSink) setBaseFeeGwei(fee float64) {
	metrics.SetEVMBaseFeeSnapshot(fee)
}

func (productionEVMSink) setPriorityFeeGwei(fee float64, observe bool) {
	metrics.SetEVMPriorityFeeSnapshot(fee, observe)
}

func (productionEVMSink) recordTxCount(count int) {
	metrics.RecordEVMTxCount(count)
}

func (productionEVMSink) incrementTxType(txType string) {
	metrics.IncrementEVMBoundedTxType(txType)
}

func (productionEVMSink) incrementTxShape(shape string, count uint64) {
	metrics.IncrementEVMTxShape(shape, count)
}

func (productionEVMSink) incrementRecipient(address string) {
	metrics.IncrementEVMRecipientTx(address)
}

func (productionEVMSink) incrementReceiptOutcome(outcome string, count uint64) {
	metrics.IncrementEVMReceiptOutcome(outcome, count)
}

func (productionEVMSink) markCountMismatch(height int64) {
	metrics.MarkEVMTxReceiptCountMismatch(height)
}

func (productionEVMSink) addSystemTxItems(count int) {
	metrics.AddEVMSystemTransactionItems(count)
}

func (productionEVMSink) addPrecompileCalls(outcome string, count uint64) {
	metrics.AddEVMReadPrecompileCalls(outcome, count)
}

type evmProcessor struct {
	mu            sync.Mutex
	lastBlockTime time.Time
	lastSource    time.Time
	sink          evmMetricSink
	recipients    *recipientLimiter
}

func newEVMProcessor(sink evmMetricSink, recipientMetrics bool, recipientLimit int) *evmProcessor {
	return &evmProcessor{
		sink:       sink,
		recipients: newRecipientLimiter(recipientMetrics, recipientLimit),
	}
}

func (p *evmProcessor) processLine(line string) error {
	obs, err := streamevm.ParseLine([]byte(line), streamevm.FullOptions())
	if err != nil {
		return err
	}
	gasUsed, gasLimit, gasRatio, err := streamevm.GasValues(obs.GasUsed, obs.GasLimit)
	if err != nil {
		return fmt.Errorf("convert gas metrics: %w", err)
	}
	baseFeeGwei, err := streamevm.WeiToGwei(obs.BaseFee)
	if err != nil {
		return fmt.Errorf("convert base fee: %w", err)
	}
	priorityFeeGwei, err := streamevm.WeiToGwei(obs.MaxPriorityFeeWei)
	if err != nil {
		return fmt.Errorf("convert priority fee: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.sink.setBlockHeight(obs.Height)
	if !p.lastBlockTime.IsZero() && obs.SourceTime.After(p.lastBlockTime) {
		delta := obs.SourceTime.Sub(p.lastBlockTime)
		p.sink.recordBlockTimeMilliseconds(float64(delta) / float64(time.Millisecond))
	}
	if p.lastBlockTime.IsZero() || obs.SourceTime.After(p.lastBlockTime) {
		p.lastBlockTime = obs.SourceTime
	}
	p.lastSource = obs.SourceTime
	p.sink.setLatestBlockTime(obs.SourceTime.Unix())
	p.sink.setGasSnapshot(gasUsed, gasLimit, gasRatio)
	p.sink.setBaseFeeGwei(baseFeeGwei)
	p.sink.setPriorityFeeGwei(priorityFeeGwei, obs.HasPriorityFee)
	p.sink.recordTxCount(obs.TransactionCount)

	shapeCounts := map[string]uint64{
		streamevm.TxShapeCreate:  0,
		streamevm.TxShapeMessage: 0,
		streamevm.TxShapeUnknown: 0,
	}
	for _, tx := range obs.Transactions {
		p.sink.incrementTxType(tx.Type)
		shapeCounts[tx.Shape]++
		if tx.Shape == streamevm.TxShapeMessage && p.recipients.enabled {
			p.sink.incrementRecipient(p.recipients.label(tx.Recipient))
		}
	}
	for _, shape := range []string{streamevm.TxShapeCreate, streamevm.TxShapeMessage, streamevm.TxShapeUnknown} {
		p.sink.incrementTxShape(shape, shapeCounts[shape])
	}
	if obs.ReceiptsEnabled {
		p.sink.incrementReceiptOutcome("success", obs.ReceiptOutcomes.Success)
		p.sink.incrementReceiptOutcome("failed", obs.ReceiptOutcomes.Failed)
		p.sink.incrementReceiptOutcome("unknown", obs.ReceiptOutcomes.Unknown)
		if obs.ReceiptCountMismatch {
			p.sink.markCountMismatch(obs.Height)
		}
	}
	if obs.SystemTxsEnabled {
		p.sink.addSystemTxItems(obs.SystemTxCount)
	}
	if obs.PrecompileCallsEnabled {
		p.sink.addPrecompileCalls("ok", obs.PrecompileOutcomes.OK)
		p.sink.addPrecompileCalls("other", obs.PrecompileOutcomes.Other)
	}
	return nil
}

func (p *evmProcessor) lastSourceTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastSource
}

// processEVMBlockAndReceiptsLine is retained as the package-level parsing
// entrypoint used by focused tests and older integrations. StartEVMMonitor uses
// its own processor so independent monitor runs never share interval state.
var defaultEVMProcessor = newEVMProcessor(productionEVMSink{}, false, 0)

func processEVMBlockAndReceiptsLine(line string) error {
	return defaultEVMProcessor.processLine(line)
}

type recipientLimiter struct {
	mu      sync.Mutex
	enabled bool
	limit   int
	seen    map[string]struct{}
}

func newRecipientLimiter(enabled bool, limit int) *recipientLimiter {
	if limit <= 0 {
		limit = defaultRecipientMetricLimit
	}
	return &recipientLimiter{
		enabled: enabled,
		limit:   limit,
		seen:    make(map[string]struct{}, limit),
	}
}

func (l *recipientLimiter) label(address string) string {
	if !l.enabled {
		return "other"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[address]; ok {
		return address
	}
	if len(l.seen) >= l.limit {
		return "other"
	}
	l.seen[address] = struct{}{}
	return address
}
