package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/exporter"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: hl_exporter <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  start    Start the Hyperliquid exporter")
		fmt.Println("  vals     Validator CSV/IP utilities (hl_exporter vals -h)")
		fmt.Println("  version  Print build information and exit")
		fmt.Println("\nOptions for start:")
		fmt.Println("  --chain                   Chain: 'mainnet' or 'testnet' (required with --otlp)")
		fmt.Println("  --node-home               Node home directory (default $NODE_HOME or ~/hl)")
		fmt.Println("  --node-binary             Node binary path (default $NODE_BINARY or $BINARY_HOME/hl-node)")
		fmt.Println("  --metrics-port            Prometheus listen port (default 8086)")
		fmt.Println("  --log-level               debug, info, warning, error (default info)")
		fmt.Println("  --evm-metrics             Enable EVM monitoring")
		fmt.Println("  --contract-metrics        Enable per-contract transaction metrics")
		fmt.Println("  --contract-metrics-limit  Max distinct contract series; the rest roll into \"other\" (default 20)")
		fmt.Println("  --replica-metrics         Enable replica-cmds transaction metrics")
		fmt.Println("  --validator-rtt           Enable validator RTT probing (outbound TCP; off by default)")
		fmt.Println("  --probe-info-endpoint     Actively probe the node's --serve-info endpoint")
		fmt.Println("  --info-endpoint-url       Info probe URL (default http://127.0.0.1:3001/info)")
		fmt.Println("  --extended-metrics        Enable the extended monitor bundle")
		fmt.Println("  --per-peer-metrics        Emit per-peer first/last-seen gauges")
		fmt.Println("  --skip-version-check      Skip the local hl-node --version probe")
		fmt.Println("  --skip-update-check       Skip the upstream hl-visor up-to-date check")
		fmt.Println("  --otlp                    Enable OTLP export (with --otlp-endpoint, --otlp-insecure, --alias)")
		fmt.Println("  --pprof                   Expose /debug/pprof/ on the metrics listener")
		os.Exit(1)
	}

	if os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v" {
		fmt.Printf("hl_exporter %s (commit %s, %s)\n",
			metrics.BuildVersion, metrics.BuildCommit, runtime.Version())
		return
	}

	if os.Args[1] == "vals" {
		runVals(os.Args[2:])
		return
	}

	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	logLevel := startCmd.String("log-level", "info", "Log level (debug, info, warning, error)")
	enableOTLP := startCmd.Bool("otlp", false, "Enable OTLP export")
	otlpEndpoint := startCmd.String("otlp-endpoint", "", "OTLP endpoint (required when OTLP is enabled)")
	nodeHome := startCmd.String("node-home", "", "Node home directory (overrides env var)")
	nodeBinary := startCmd.String("node-binary", "", "Node binary path (overrides env var)")
	alias := startCmd.String("alias", "", "Node alias (required when OTLP is enabled)")
	chain := startCmd.String("chain", "", "Chain type (required when OTLP is enabled: 'mainnet' or 'testnet')")
	otlpInsecure := startCmd.Bool("otlp-insecure", false, "Use insecure connection for OTLP")
	enableEVM := startCmd.Bool("evm-metrics", false, "Enable EVM monitoring")
	contractMetrics := startCmd.Bool("contract-metrics", false, "Enable per-contract transaction metrics")
	contractLimit := startCmd.Int("contract-metrics-limit", 20, "Maximum number of individual contract labels to retain")
	enableReplicaMetrics := startCmd.Bool("replica-metrics", false, "Enable replica commands transaction metrics")
	enableValidatorRTT := startCmd.Bool("validator-rtt", false, "Enable validator RTT monitoring")
	skipVersionCheck := startCmd.Bool("skip-version-check", false, "Skip the local hl-node --version probe (use when running in a container without the binary)")
	skipUpdateCheck := startCmd.Bool("skip-update-check", false, "Skip the periodic upstream binary download for the up-to-date check")
	metricsPort := startCmd.Int("metrics-port", 8086, "Port to expose Prometheus metrics on")
	probeInfoEndpoint := startCmd.Bool("probe-info-endpoint", false, "Actively HTTP-probe the node's --serve-info endpoint as a liveness check")
	infoEndpointURL := startCmd.String("info-endpoint-url", "", "URL the info probe POSTs to (default http://127.0.0.1:3001/info)")
	enableExtendedMetrics := startCmd.Bool("extended-metrics", false, "Enable the extended monitor set (tcp_lz4, log lines, public IP, Tokio runtime, operator config, tmp dir)")
	enablePerPeerMetrics := startCmd.Bool("per-peer-metrics", false, "Emit hl_p2p_peer_{last,first}_seen_seconds{ip} per known peer (cardinality bounded by the peer set's LRU cap + 24h TTL)")
	enablePprof := startCmd.Bool("pprof", false, "Expose Go profiling endpoints under /debug/pprof/ on the metrics listener")

	switch os.Args[1] {
	case "start":
		startCmd.Parse(os.Args[2:])
	default:
		fmt.Printf("%q is not a valid command.\n", os.Args[1])
		os.Exit(1)
	}

	if err := logger.SetLogLevel(*logLevel); err != nil {
		fmt.Printf("Error setting log level: %v\n", err)
		os.Exit(1)
	}

	if *chain != "" {
		*chain = strings.ToLower(*chain)
		if *chain != "mainnet" && *chain != "testnet" {
			logger.Error("--chain flag must be either 'mainnet' or 'testnet' (case insensitive)")
			os.Exit(1)
		}
		*chain = strings.ToLower(*chain)
	}

	flags := &config.Flags{
		NodeHome:              *nodeHome,
		NodeBinary:            *nodeBinary,
		Chain:                 *chain,
		EnableEVM:             *enableEVM,
		EnableContractMetrics: *contractMetrics,
		ContractMetricsLimit:  *contractLimit,
		EnableCoreTxMetrics:   false,
		UseLiveState:          false,
		EnableReplicaMetrics:  *enableReplicaMetrics,
		ReplicaDataDir:        "",                 // Always use default
		ReplicaBufferSize:     8,                  // Always use default 8MB
		EVMBlockTypeMetrics:   *enableEVM,         // Always enable block type metrics when EVM is enabled
		EnableValidatorRTT:    enableValidatorRTT, // Use the bool pointer directly
		SkipVersionCheck:      *skipVersionCheck,
		SkipUpdateCheck:       *skipUpdateCheck,
		ProbeInfoEndpoint:     *probeInfoEndpoint,
		InfoEndpointURL:       *infoEndpointURL,
		EnableExtendedMetrics: *enableExtendedMetrics,
		EnablePerPeerMetrics:  *enablePerPeerMetrics,
		EnablePprof:           *enablePprof,
	}

	cfg := config.LoadConfig(flags)

	if *enableOTLP {
		if *alias == "" {
			logger.Error("--alias flag is required when OTLP is enabled. This can be whatever you choose and is just an identifier for your node.")
			os.Exit(1)
		}
		if *chain == "" {
			logger.Error("--chain flag is required when OTLP is enabled")
			os.Exit(1)
		}
		if *otlpEndpoint == "" {
			logger.Error("--otlp-endpoint flag is required when OTLP is enabled")
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// After loading config, before metrics initialization
	validatorAddress, isValidator := monitors.GetValidatorStatus(cfg.NodeHome)

	// Pre-populate signer mappings before any monitors start
	if err := monitors.PopulateSignerMappings(cfg.NodeHome); err != nil {
		logger.Warning("Failed to pre-populate signer mappings: %v", err)
		// Non-fatal - continue startup
	}

	// Initialize metrics configuration
	metricsConfig := metrics.MetricsConfig{
		EnablePrometheus: true, // Always enable Prometheus - it's the core functionality
		EnableOTLP:       *enableOTLP,
		OTLPEndpoint:     *otlpEndpoint,
		OTLPInsecure:     *otlpInsecure,
		Alias:            *alias,
		Chain:            *chain,
		NodeHome:         cfg.NodeHome,
		ValidatorAddress: validatorAddress,
		IsValidator:      isValidator,
		EnableEVM:        *enableEVM,
		PrometheusPort:   *metricsPort,
		EnablePprof:      *enablePprof,
	}

	if err := metrics.InitMetrics(ctx, metricsConfig); err != nil {
		logger.Error("Failed to initialize metrics: %v", err)
		os.Exit(1)
	}

	exporter.Start(ctx, cfg)

	<-ctx.Done()
	logger.InfoComponent("system", "Shutting down gracefully")
}
