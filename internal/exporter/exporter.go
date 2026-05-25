package exporter

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func Start(ctx context.Context, cfg config.Config) {
	logger.InfoComponent("system", "Starting Hyperliquid exporter...")

	// create context with cancellation for monitor goroutines
	monitorCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// start monitors with error channels
	blockErrCh := make(chan error, 1)
	proposalErrCh := make(chan error, 1)
	validatorErrCh := make(chan error, 1)
	versionErrCh := make(chan error, 1)
	updateErrCh := make(chan error, 1)
	evmErrCh := make(chan error, 1)
	evmTxsErrCh := make(chan error, 1)
	evmAccountErrCh := make(chan error, 1)
	validatorStatusErrCh := make(chan error, 1)
	validatorIPErrCh := make(chan error, 1)
	coreTxErrCh := make(chan error, 1)
	consensusErrCh := make(chan error, 1)
	replicaErrCh := make(chan error, 1)
	latencyErrCh := make(chan error, 1)
	gossipErrCh := make(chan error, 1)
	visorErrCh := make(chan error, 1)
	subsystemLatencyErrCh := make(chan error, 1)
	critMsgErrCh := make(chan error, 1)
	tcpTrafficErrCh := make(chan error, 1)
	parentPeerErrCh := make(chan error, 1)
	diskErrCh := make(chan error, 1)
	processErrCh := make(chan error, 1)
	gossipConnectionsErrCh := make(chan error, 1)
	snapshotStatusErrCh := make(chan error, 1)
	nodeStateErrCh := make(chan error, 1)
	infoProbeErrCh := make(chan error, 1)
	tcpLz4ErrCh := make(chan error, 1)
	logLinesErrCh := make(chan error, 1)
	publicIPErrCh := make(chan error, 1)
	tokioErrCh := make(chan error, 1)
	operatorConfigErrCh := make(chan error, 1)
	tmpDirErrCh := make(chan error, 1)
	tcpConnectionsErrCh := make(chan error, 1)
	replicaRunsErrCh := make(chan error, 1)
	rocksDBErrCh := make(chan error, 1)
	subsystemStepsErrCh := make(chan error, 1)
	critLocationsErrCh := make(chan error, 1)

	logger.InfoComponent("core", "Initializing block monitor...")
	runMonitor("block", func() { monitors.StartBlockMonitor(monitorCtx, cfg, blockErrCh) })

	// start val API monitor early to populate signer mappings
	logger.InfoComponent("consensus", "Initializing validator API monitor...")
	runMonitor("validator_api", func() { monitors.StartValidatorMonitor(monitorCtx, cfg, validatorErrCh) })

	// only initialize proposal monitor if replica metrics are disabled
	if !cfg.EnableReplicaMetrics {
		logger.InfoComponent("consensus", "Initializing proposal monitor...")
		runMonitor("proposal", func() { monitors.StartProposalMonitor(monitorCtx, cfg, proposalErrCh) })
	} else {
		logger.InfoComponent("consensus", "Proposal monitor disabled - replica monitor will handle proposer counting")
	}

	if cfg.SkipVersionCheck {
		logger.InfoComponent("system", "Version monitor disabled by --skip-version-check")
	} else if _, err := monitors.NodeBinaryReady(cfg); err != nil {
		logger.WarningComponent("system", "Skipping version monitor: %v (use --skip-version-check to silence)", err)
	} else {
		logger.InfoComponent("system", "Initializing version monitor...")
		runMonitor("version", func() { monitors.StartVersionMonitor(monitorCtx, cfg, versionErrCh) })
	}

	if cfg.SkipUpdateCheck {
		logger.InfoComponent("system", "Update checker disabled by --skip-update-check")
	} else {
		logger.InfoComponent("system", "Initializing update checker...")
		runMonitor("update_checker", func() { monitors.StartUpdateChecker(monitorCtx, cfg, updateErrCh) })
	}

	if cfg.EnableEVM {
		logger.InfoComponent("evm", "Initializing EVM monitor (using evm_block_and_receipts)...")
		runMonitor("evm", func() { monitors.StartEVMMonitor(monitorCtx, cfg, evmErrCh) })

		logger.InfoComponent("evm", "Initializing EVM Account monitor...")
		runMonitor("evm_account", func() { monitors.StartEVMAccountMonitor(monitorCtx, cfg, evmAccountErrCh) })
	}

	logger.InfoComponent("consensus", "Initializing Validator Status monitor...")
	monitors.StartValidatorStatusMonitor(ctx, cfg, validatorStatusErrCh)

	if cfg.EnableValidatorRTT {
		logger.InfoComponent("consensus", "Initializing validator IP monitor...")
		runMonitor("validator_ip", func() { monitors.StartValidatorIPMonitor(monitorCtx, cfg, validatorIPErrCh) })
	} else {
		logger.InfoComponent("consensus", "Validator IP monitor disabled")
	}

	logger.InfoComponent("consensus", "Initializing comprehensive consensus monitor...")
	runMonitor("consensus", func() { monitors.StartConsensusMonitor(monitorCtx, &cfg, consensusErrCh) })

	// start validator latency monitor (only runs on validator nodes)
	logger.InfoComponent("latency", "Initializing validator latency monitor...")
	runMonitor("validator_latency", func() { monitors.StartValidatorLatencyMonitor(monitorCtx, &cfg, latencyErrCh) })

	// start gossip monitor (only runs if gossip_rpc logs exist)
	logger.InfoComponent("gossip", "Initializing gossip monitor...")
	runMonitor("gossip", func() { monitors.StartGossipMonitor(monitorCtx, &cfg, gossipErrCh) })

	// visor sync-state monitor (always on — applies to all node roles)
	logger.InfoComponent("visor", "Initializing visor state monitor...")
	runMonitor("visor", func() { monitors.StartVisorMonitor(monitorCtx, cfg, visorErrCh) })

	// per-subsystem latency monitor (always on)
	logger.InfoComponent("subsystem_latency", "Initializing subsystem latency monitor...")
	runMonitor("subsystem_latency", func() { monitors.StartSubsystemLatencyMonitor(monitorCtx, cfg, subsystemLatencyErrCh) })

	// crit_msg_stats monitor (always on — alerts on fatal node messages)
	logger.InfoComponent("crit_msg", "Initializing critical-message monitor...")
	runMonitor("crit_msg", func() { monitors.StartCritMsgMonitor(monitorCtx, cfg, critMsgErrCh) })

	// tcp_traffic monitor (per-peer throughput, top-N bounded)
	logger.InfoComponent("tcp_traffic", "Initializing tcp_traffic monitor...")
	runMonitor("tcp_traffic", func() { monitors.StartTCPTrafficMonitor(monitorCtx, cfg, tcpTrafficErrCh) })

	// parent peer detection — depends on the tcp_traffic monitor publishing
	// its snapshot, so launched after it. No file IO of its own.
	logger.InfoComponent("parent_peer", "Initializing parent peer monitor...")
	runMonitor("parent_peer", func() { monitors.StartParentPeerMonitor(monitorCtx, cfg, parentPeerErrCh) })

	// disk usage of NODE_HOME and its filesystem
	logger.InfoComponent("disk", "Initializing disk usage monitor...")
	runMonitor("disk", func() { monitors.StartDiskMonitor(monitorCtx, cfg, diskErrCh) })

	// hl-node/hl-visor process resource usage (Linux /proc)
	logger.InfoComponent("process", "Initializing process monitor...")
	runMonitor("process", func() { monitors.StartProcessMonitor(monitorCtx, cfg, processErrCh) })

	// gossip-connection event counter (peer churn, max-peers-reached, etc.)
	logger.InfoComponent("gossip_connections", "Initializing gossip-connections monitor...")
	runMonitor("gossip_connections", func() { monitors.StartGossipConnectionsMonitor(monitorCtx, cfg, gossipConnectionsErrCh) })

	// Phase A — always-on essentials.
	logger.InfoComponent("snapshot_status", "Initializing snapshot-status monitor...")
	runMonitor("snapshot_status", func() { monitors.StartSnapshotStatusMonitor(monitorCtx, cfg, snapshotStatusErrCh) })

	logger.InfoComponent("node_state", "Initializing node-state monitor...")
	runMonitor("node_state", func() { monitors.StartNodeStateMonitor(monitorCtx, cfg, nodeStateErrCh) })

	logger.InfoComponent("tcp_connections", "Initializing TCP-connections monitor...")
	runMonitor("tcp_connections", func() { monitors.StartTCPConnectionsMonitor(monitorCtx, cfg, tcpConnectionsErrCh) })

	logger.InfoComponent("replica_runs", "Initializing replica-runs monitor...")
	runMonitor("replica_runs", func() { monitors.StartReplicaRunsMonitor(monitorCtx, cfg, replicaRunsErrCh) })

	// Phase B — gated by --probe-info-endpoint.
	if cfg.ProbeInfoEndpoint {
		logger.InfoComponent("info_probe", "Initializing info-endpoint probe...")
		runMonitor("info_probe", func() { monitors.StartInfoProbeMonitor(monitorCtx, cfg, infoProbeErrCh) })
	} else {
		logger.InfoComponent("info_probe", "Info-endpoint probe disabled (--probe-info-endpoint to enable)")
	}

	// Phase C — gated by --extended-metrics.
	if cfg.EnableExtendedMetrics {
		logger.InfoComponent("tcp_lz4", "Initializing tcp_lz4 monitor...")
		runMonitor("tcp_lz4", func() { monitors.StartTCPLz4Monitor(monitorCtx, cfg, tcpLz4ErrCh) })

		logger.InfoComponent("log_lines", "Initializing log-lines monitor...")
		runMonitor("log_lines", func() { monitors.StartLogLinesMonitor(monitorCtx, cfg, logLinesErrCh) })

		logger.InfoComponent("public_ip", "Initializing public-IP monitor...")
		runMonitor("public_ip", func() { monitors.StartPublicIPMonitor(monitorCtx, cfg, publicIPErrCh) })

		logger.InfoComponent("tokio_runtime", "Initializing Tokio runtime monitor...")
		runMonitor("tokio_runtime", func() { monitors.StartTokioRuntimeMonitor(monitorCtx, cfg, tokioErrCh) })

		logger.InfoComponent("operator_config", "Initializing operator-config monitor...")
		runMonitor("operator_config", func() { monitors.StartOperatorConfigMonitor(monitorCtx, cfg, operatorConfigErrCh) })

		logger.InfoComponent("tmp_dir", "Initializing tmp-dir monitor...")
		runMonitor("tmp_dir", func() { monitors.StartTmpDirMonitor(monitorCtx, cfg, tmpDirErrCh) })

		logger.InfoComponent("rocksdb", "Initializing RocksDB monitor...")
		runMonitor("rocksdb", func() { monitors.StartRocksDBMonitor(monitorCtx, cfg, rocksDBErrCh) })

		logger.InfoComponent("subsystem_steps", "Initializing per-step subsystem latency monitor...")
		runMonitor("subsystem_steps", func() { monitors.StartSubsystemStepsMonitor(monitorCtx, cfg, subsystemStepsErrCh) })

		logger.InfoComponent("crit_locations", "Initializing crit-locations monitor...")
		runMonitor("crit_locations", func() { monitors.StartCritLocationsMonitor(monitorCtx, cfg, critLocationsErrCh) })
	} else {
		logger.InfoComponent("extended_metrics", "Extended metrics disabled (--extended-metrics to enable)")
	}

	if cfg.EnableReplicaMetrics {
		logger.InfoComponent("replica", "Initializing replica commands monitor (streaming)...")
		replicaMonitor := monitors.NewReplicaMonitor(cfg.ReplicaDataDir, cfg.ReplicaBufferSize)
		runMonitor("replica", func() {
			if err := replicaMonitor.Start(monitorCtx); err != nil {
				replicaErrCh <- err
			}
		})
	}

	logger.InfoComponent("system", "Exporter is now running")

	// start memory monitoring
	runMonitor("memory", func() { metrics.StartMemoryMonitoring(monitorCtx) })

	// start metrics cleanup to prevent unbounded map growth
	metrics.StartMetricsCleanup()
	defer metrics.StopMetricsCleanup()

	// publish exporter build_info once and refresh the per-monitor health
	// snapshot every 10s. Background goroutine so the values stay current
	// without blocking the main error loop.
	metrics.SetBuildInfo()
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		metrics.PublishMonitorHealthSnapshot()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-t.C:
				metrics.PublishMonitorHealthSnapshot()
			}
		}
	}()

	for {
		select {
		case err := <-blockErrCh:
			metrics.IncMonitorError("block")
			logger.ErrorComponent("core", "Block monitor error: %v", err)
		case err := <-proposalErrCh:
			metrics.IncMonitorError("proposal")
			logger.ErrorComponent("consensus", "Proposal monitor error: %v", err)
		case err := <-validatorErrCh:
			metrics.IncMonitorError("validator_api")
			logger.ErrorComponent("consensus", "Validator monitor error: %v", err)
		case err := <-versionErrCh:
			metrics.IncMonitorError("version")
			logger.ErrorComponent("system", "Version monitor error: %v", err)
		case err := <-updateErrCh:
			metrics.IncMonitorError("update_checker")
			logger.ErrorComponent("system", "Update checker error: %v", err)
		case err := <-evmErrCh:
			metrics.IncMonitorError("evm")
			logger.ErrorComponent("evm", "EVM Monitor error: %v", err)
		case err := <-evmTxsErrCh:
			// This channel is no longer used but kept for compatibility
			logger.ErrorComponent("evm", "Legacy EVM Transactions Monitor error (should not happen): %v", err)
		case err := <-evmAccountErrCh:
			metrics.IncMonitorError("evm_account")
			logger.ErrorComponent("evm", "EVM Account Monitor error: %v", err)
		case err := <-validatorStatusErrCh:
			metrics.IncMonitorError("validator_status")
			logger.ErrorComponent("consensus", "Validator Status Monitor error: %v", err)
		case err := <-validatorIPErrCh:
			metrics.IncMonitorError("validator_ip")
			logger.ErrorComponent("consensus", "Validator IP Monitor error: %v", err)
		case err := <-coreTxErrCh:
			metrics.IncMonitorError("core_tx")
			logger.ErrorComponent("core", "Core TX Monitor error: %v", err)
		case err := <-consensusErrCh:
			metrics.IncMonitorError("consensus")
			logger.ErrorComponent("consensus", "Consensus monitor error: %v", err)
		case err := <-replicaErrCh:
			metrics.IncMonitorError("replica")
			logger.ErrorComponent("replica", "Replica monitor error: %v", err)
		case err := <-latencyErrCh:
			metrics.IncMonitorError("validator_latency")
			logger.ErrorComponent("latency", "Validator latency monitor error: %v", err)
		case err := <-gossipErrCh:
			metrics.IncMonitorError("gossip")
			logger.ErrorComponent("gossip", "Gossip monitor error: %v", err)
		case err := <-visorErrCh:
			metrics.IncMonitorError("visor")
			logger.ErrorComponent("visor", "Visor monitor error: %v", err)
		case err := <-subsystemLatencyErrCh:
			metrics.IncMonitorError("subsystem_latency")
			logger.ErrorComponent("subsystem_latency", "Subsystem latency monitor error: %v", err)
		case err := <-critMsgErrCh:
			metrics.IncMonitorError("crit_msg")
			logger.ErrorComponent("crit_msg", "Critical message monitor error: %v", err)
		case err := <-tcpTrafficErrCh:
			metrics.IncMonitorError("tcp_traffic")
			logger.ErrorComponent("tcp_traffic", "TCP traffic monitor error: %v", err)
		case err := <-parentPeerErrCh:
			metrics.IncMonitorError("parent_peer")
			logger.ErrorComponent("parent_peer", "Parent peer monitor error: %v", err)
		case err := <-diskErrCh:
			metrics.IncMonitorError("disk")
			logger.ErrorComponent("disk", "Disk monitor error: %v", err)
		case err := <-processErrCh:
			metrics.IncMonitorError("process")
			logger.ErrorComponent("process", "Process monitor error: %v", err)
		case err := <-gossipConnectionsErrCh:
			metrics.IncMonitorError("gossip_connections")
			logger.ErrorComponent("gossip_connections", "Gossip-connections monitor error: %v", err)
		case err := <-snapshotStatusErrCh:
			metrics.IncMonitorError("snapshot_status")
			logger.ErrorComponent("snapshot_status", "Snapshot-status monitor error: %v", err)
		case err := <-nodeStateErrCh:
			metrics.IncMonitorError("node_state")
			logger.ErrorComponent("node_state", "Node-state monitor error: %v", err)
		case err := <-infoProbeErrCh:
			metrics.IncMonitorError("info_probe")
			logger.ErrorComponent("info_probe", "Info-endpoint probe error: %v", err)
		case err := <-tcpLz4ErrCh:
			metrics.IncMonitorError("tcp_lz4")
			logger.ErrorComponent("tcp_lz4", "tcp_lz4 monitor error: %v", err)
		case err := <-logLinesErrCh:
			metrics.IncMonitorError("log_lines")
			logger.ErrorComponent("log_lines", "log-lines monitor error: %v", err)
		case err := <-publicIPErrCh:
			metrics.IncMonitorError("public_ip")
			logger.ErrorComponent("public_ip", "public-IP monitor error: %v", err)
		case err := <-tokioErrCh:
			metrics.IncMonitorError("tokio_runtime")
			logger.ErrorComponent("tokio_runtime", "Tokio runtime monitor error: %v", err)
		case err := <-operatorConfigErrCh:
			metrics.IncMonitorError("operator_config")
			logger.ErrorComponent("operator_config", "Operator-config monitor error: %v", err)
		case err := <-tmpDirErrCh:
			metrics.IncMonitorError("tmp_dir")
			logger.ErrorComponent("tmp_dir", "tmp-dir monitor error: %v", err)
		case err := <-tcpConnectionsErrCh:
			metrics.IncMonitorError("tcp_connections")
			logger.ErrorComponent("tcp_connections", "tcp-connections monitor error: %v", err)
		case err := <-replicaRunsErrCh:
			metrics.IncMonitorError("replica_runs")
			logger.ErrorComponent("replica_runs", "replica-runs monitor error: %v", err)
		case err := <-rocksDBErrCh:
			metrics.IncMonitorError("rocksdb")
			logger.ErrorComponent("rocksdb", "RocksDB monitor error: %v", err)
		case err := <-subsystemStepsErrCh:
			metrics.IncMonitorError("subsystem_steps")
			logger.ErrorComponent("subsystem_steps", "subsystem-steps monitor error: %v", err)
		case err := <-critLocationsErrCh:
			metrics.IncMonitorError("crit_locations")
			logger.ErrorComponent("crit_locations", "crit-locations monitor error: %v", err)
		case <-ctx.Done():
			logger.InfoComponent("system", "Shutting down monitors...")
			return
		}
		// small sleep to prevent tight loop in case of repeated errors
		time.Sleep(100 * time.Millisecond)
	}
}
