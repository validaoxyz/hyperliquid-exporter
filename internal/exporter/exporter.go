package exporter

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func Start(ctx context.Context, cfg config.Config) {
	logger.Info("Starting Hyperliquid exporter...")

	// Create a context with cancellation for monitor goroutines
	monitorCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start monitors with error channels
	blockErrCh := make(chan error, 1)
	proposalErrCh := make(chan error, 1)
	validatorErrCh := make(chan error, 1)
	versionErrCh := make(chan error, 1)
	updateErrCh := make(chan error, 1)
	evmErrCh := make(chan error) // Add error channel for the EVM Monitor
	evmTxsErrCh := make(chan error)
	validatorStatusErrCh := make(chan error)
	validatorIPErrCh := make(chan error, 1)

	logger.Info("Initializing block monitor...")
	go monitors.StartBlockMonitor(monitorCtx, cfg, blockErrCh)

	logger.Info("Initializing proposal monitor...")
	go monitors.StartProposalMonitor(monitorCtx, cfg, proposalErrCh)

	logger.Info("Initializing version monitor...")
	go monitors.StartVersionMonitor(monitorCtx, cfg, versionErrCh)

	logger.Info("Initializing update checker...")
	go monitors.StartUpdateChecker(monitorCtx, updateErrCh)

	logger.Info("Initializing evm monitor...")
	go monitors.StartEVMBlockHeightMonitor(monitorCtx, cfg, evmErrCh) // Start the EVM Monitor

	logger.Info("Initializing EVM Transactions monitor...")
	go monitors.StartEVMTransactionsMonitor(monitorCtx, cfg, evmTxsErrCh)

	logger.Info("Initializing Validator Status monitor...")
	monitors.StartValidatorStatusMonitor(ctx, cfg, validatorStatusErrCh)

	logger.Info("Initializing validator IP monitor...")
	go monitors.StartValidatorIPMonitor(monitorCtx, cfg, validatorIPErrCh)

	logger.Info("Initializing validator API monitor...")
	go monitors.StartValidatorMonitor(monitorCtx, validatorErrCh)

	logger.Info("Exporter is now running")

	for {
		select {
		case err := <-blockErrCh:
			logger.Error("Block monitor error: %v", err)
		case err := <-proposalErrCh:
			logger.Error("Proposal monitor error: %v", err)
		case err := <-validatorErrCh:
			logger.Error("Validator monitor error: %v", err)
		case err := <-versionErrCh:
			logger.Error("Version monitor error: %v", err)
		case err := <-updateErrCh:
			logger.Error("Update checker error: %v", err)
		case err := <-evmErrCh:
			logger.Error("EVM Monitor error: %v", err)
		case err := <-evmTxsErrCh:
			logger.Error("EVM Transactions Monitor error: %v", err)
		case err := <-validatorStatusErrCh:
			logger.Error("Validator Status Monitor error: %v", err)
		case err := <-validatorIPErrCh:
			logger.Error("Validator IP Monitor error: %v", err)
		case <-ctx.Done():
			logger.Info("Shutting down monitors...")
			return
		}
		// small sleep to prevent tight loop in case of repeated errors
		time.Sleep(100 * time.Millisecond)
	}
}
