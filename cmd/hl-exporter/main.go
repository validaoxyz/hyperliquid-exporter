package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/exporter"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

const metricsShutdownTimeout = 10 * time.Second

type startOptions struct {
	logLevel             *string
	enableOTLP           *bool
	otlpEndpoint         *string
	nodeHome             *string
	nodeBinary           *string
	alias                *string
	chain                *string
	otlpInsecure         *bool
	enableEVM            *bool
	contractMetrics      *bool
	contractLimit        *int
	enableReplicaMetrics *bool
	enableValidatorRTT   *bool
	skipVersionCheck     *bool
	skipUpdateCheck      *bool
	metricsPort          *int
	probeInfoEndpoint    *bool
	infoEndpointURL      *string
	enableExtended       *bool
	enablePerPeer        *bool
	tcpServicePorts      *string
	disableTCP6          *bool
	enablePprof          *bool
}

func newStartFlagSet(errorHandling flag.ErrorHandling, output io.Writer) (*flag.FlagSet, *startOptions) {
	fs := flag.NewFlagSet("start", errorHandling)
	fs.SetOutput(output)
	o := &startOptions{}
	o.logLevel = fs.String("log-level", "info", "Log level (debug, info, warning, error)")
	o.enableOTLP = fs.Bool("otlp", false, "Enable OTLP export")
	o.otlpEndpoint = fs.String("otlp-endpoint", "", "OTLP endpoint (required when OTLP is enabled)")
	o.nodeHome = fs.String("node-home", "", "Node home directory (overrides env var)")
	o.nodeBinary = fs.String("node-binary", "", "Node binary path (overrides env var)")
	o.alias = fs.String("alias", "", "Node alias (required when OTLP is enabled)")
	o.chain = fs.String("chain", "", "Chain type (required: 'mainnet' or 'testnet')")
	o.otlpInsecure = fs.Bool("otlp-insecure", false, "Use insecure connection for OTLP")
	o.enableEVM = fs.Bool("evm-metrics", false, "Enable EVM monitoring")
	o.contractMetrics = fs.Bool("contract-metrics", false, "Enable canonical recipient-address diagnostics; no contract identity or enrichment is inferred")
	o.contractLimit = fs.Int("contract-metrics-limit", 20, "Maximum canonical recipient addresses to retain before using address=other")
	o.enableReplicaMetrics = fs.Bool("replica-metrics", false, "Enable replica commands transaction metrics")
	o.enableValidatorRTT = fs.Bool("validator-rtt", false, "Enable outbound TCP-connect diagnostics for eligible validators; not protocol RTT")
	o.skipVersionCheck = fs.Bool("skip-version-check", false, "Skip the local hl-node --version probe (use when running in a container without the binary)")
	o.skipUpdateCheck = fs.Bool("skip-update-check", false, "Skip the periodic upstream binary download for the up-to-date check")
	o.metricsPort = fs.Int("metrics-port", 8086, "Port to expose Prometheus metrics on")
	o.probeInfoEndpoint = fs.Bool("probe-info-endpoint", false, "Actively HTTP-probe the node's --serve-info endpoint as a liveness check")
	o.infoEndpointURL = fs.String("info-endpoint-url", "", "URL the info probe POSTs to (default http://127.0.0.1:3001/info)")
	o.enableExtended = fs.Bool("extended-metrics", false, "Enable the extended monitor set (tcp_lz4, log lines, public IP, Tokio runtime, operator config, tmp dir)")
	o.enablePerPeer = fs.Bool("per-peer-metrics", false, "Emit up to 16 current explicit child identities from child_peers status")
	o.tcpServicePorts = fs.String("tcp-service-ports", config.DefaultTCPServicePorts, "Comma-separated tracked TCP service ports (1..16)")
	o.disableTCP6 = fs.Bool("disable-tcp6", false, "Disable reading /proc/net/tcp6 (an unavailable enabled source is reported unhealthy)")
	o.enablePprof = fs.Bool("pprof", false, "Expose Go profiling endpoints under /debug/pprof/ on the metrics listener")
	return fs, o
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: hl_exporter <command> [options]")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  start    Start the Hyperliquid exporter")
	fmt.Fprintln(w, "  vals     Validator CSV/IP utilities (hl_exporter vals -h)")
	fmt.Fprintln(w, "  version  Print build information and exit")
	fmt.Fprintln(w, "\nOptions for start:")
	fs, _ := newStartFlagSet(flag.ContinueOnError, w)
	fs.PrintDefaults()
}

func main() {
	if len(os.Args) < 2 {
		printRootUsage(os.Stdout)
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

	startCmd, options := newStartFlagSet(flag.ExitOnError, os.Stderr)

	switch os.Args[1] {
	case "start":
		startCmd.Parse(os.Args[2:])
	default:
		fmt.Printf("%q is not a valid command.\n", os.Args[1])
		os.Exit(1)
	}

	if err := logger.SetLogLevel(*options.logLevel); err != nil {
		fmt.Printf("Error setting log level: %v\n", err)
		os.Exit(1)
	}

	flags := &config.Flags{
		NodeHome:              *options.nodeHome,
		NodeBinary:            *options.nodeBinary,
		Chain:                 *options.chain,
		EnableEVM:             *options.enableEVM,
		EnableContractMetrics: *options.contractMetrics,
		ContractMetricsLimit:  *options.contractLimit,
		EnableCoreTxMetrics:   false,
		UseLiveState:          false,
		EnableReplicaMetrics:  *options.enableReplicaMetrics,
		ReplicaDataDir:        "",                         // Always use default
		ReplicaBufferSize:     8,                          // Always use default 8MB
		EnableValidatorRTT:    options.enableValidatorRTT, // Use the bool pointer directly
		SkipVersionCheck:      *options.skipVersionCheck,
		SkipUpdateCheck:       *options.skipUpdateCheck,
		ProbeInfoEndpoint:     *options.probeInfoEndpoint,
		InfoEndpointURL:       *options.infoEndpointURL,
		EnableExtendedMetrics: *options.enableExtended,
		EnablePerPeerMetrics:  *options.enablePerPeer,
		TCPServicePorts:       *options.tcpServicePorts,
		DisableTCP6:           *options.disableTCP6,
		EnablePprof:           *options.enablePprof,
	}

	cfg, err := config.LoadConfig(flags)
	if err != nil {
		logger.Error("Invalid configuration: %v", err)
		os.Exit(1)
	}

	if *options.enableOTLP {
		if *options.alias == "" {
			logger.Error("--alias flag is required when OTLP is enabled. This can be whatever you choose and is just an identifier for your node.")
			os.Exit(1)
		}
		if *options.otlpEndpoint == "" {
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
		EnableOTLP:       *options.enableOTLP,
		OTLPEndpoint:     *options.otlpEndpoint,
		OTLPInsecure:     *options.otlpInsecure,
		Alias:            *options.alias,
		Chain:            cfg.Chain,
		NodeHome:         cfg.NodeHome,
		ValidatorAddress: validatorAddress,
		IsValidator:      isValidator,
		EnableEVM:        *options.enableEVM,
		PrometheusPort:   *options.metricsPort,
		EnablePprof:      *options.enablePprof,
	}

	providerOwner, err := metrics.InitMetrics(ctx, metricsConfig)
	if err != nil {
		logger.Error("Failed to initialize metrics: %v", err)
		os.Exit(1)
	}

	exporter.Start(ctx, cfg)

	if err := shutdownMetrics(providerOwner); err != nil {
		logger.ErrorComponent("system", "Metrics provider shutdown failed: %v", err)
	}
	logger.InfoComponent("system", "Shutdown complete")
}

type metricsShutdowner interface {
	Shutdown(context.Context) error
}

func shutdownMetrics(owner metricsShutdowner) error {
	return shutdownMetricsWithin(owner, metricsShutdownTimeout)
}

func shutdownMetricsWithin(owner metricsShutdowner, timeout time.Duration) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return owner.Shutdown(shutdownCtx)
}
