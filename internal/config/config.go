package config

import (
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

type Config struct {
	HomeDir                string
	NodeHome               string
	BinaryHome             string
	NodeBinary             string
	Chain                  string
	EnableEVM              bool
	EnableContractMetrics  bool
	ContractMetricsLimit   int
	EVMBlockTypeMetrics    bool
	EnableCoreTxMetrics    bool
	UseLiveState           bool
	LiveStateCheckInterval time.Duration // How often to check for updates
	EnableReplicaMetrics   bool
	ReplicaDataDir         string
	ReplicaBufferSize      int
	EnableValidatorRTT     bool
	MetricsAddr            string
	LogLevel               string
	LogFormat              string
	// SkipVersionCheck disables the local hl-node --version probe. Set this
	// when the exporter runs in a container that doesn't have the node
	// binary on disk. Tracked in upstream issue #16.
	SkipVersionCheck bool
	// SkipUpdateCheck disables the periodic download of the latest hl-visor
	// binary from binaries.hyperliquid.xyz. Useful for restricted
	// environments and as a companion to SkipVersionCheck.
	SkipUpdateCheck bool
	// ProbeInfoEndpoint enables an active HTTP probe of the node's
	// `--serve-info` endpoint as a liveness check. Off by default
	// because some operators disallow outbound HTTP from the exporter
	// process even to localhost.
	ProbeInfoEndpoint bool
	// InfoEndpointURL is the URL the info probe POSTs to; defaults to
	// http://127.0.0.1:3001/info when empty.
	InfoEndpointURL string
	// EnableExtendedMetrics opts the exporter into the "extended" set
	// of monitors: tcp_lz4, log line counters, public-IP heartbeat,
	// Tokio task metrics, operator-config age, tmp-dir audit.
	// Useful for deep operator dashboards; off by default to keep the
	// scrape lean for the median user.
	EnableExtendedMetrics bool
}

type Flags struct {
	NodeHome              string
	NodeBinary            string
	Chain                 string
	EnableEVM             bool
	EnableContractMetrics bool
	ContractMetricsLimit  int
	EVMBlockTypeMetrics   bool
	EnableCoreTxMetrics   bool
	UseLiveState          bool
	EnableReplicaMetrics  bool
	ReplicaDataDir        string
	ReplicaBufferSize     int
	EnableValidatorRTT    *bool // to distinguish between not set and false
	MetricsAddr           string
	LogLevel              string
	LogFormat             string
	SkipVersionCheck      bool
	SkipUpdateCheck       bool
	ProbeInfoEndpoint     bool
	InfoEndpointURL       string
	EnableExtendedMetrics bool
}

// load env vars and returns a Config struct
func LoadConfig(flags *Flags) Config {
	// load .env first
	if err := godotenv.Load(); err != nil {
		logger.Debug("No .env file found, using environment variables and flags")
	}

	homeDir := os.Getenv("HOME")

	nodeHome := os.Getenv("NODE_HOME")
	if nodeHome == "" {
		nodeHome = homeDir + "/hl" //default fallback
	}

	binaryHome := os.Getenv("BINARY_HOME")
	if binaryHome == "" {
		binaryHome = homeDir
	}

	nodeBinary := os.Getenv("NODE_BINARY")
	if nodeBinary == "" {
		nodeBinary = binaryHome + "/hl-node"
	}

	// always use default replica data dir
	replicaDataDir := nodeHome + "/data/replica_cmds"

	// always default buffer size
	replicaBufferSize := 8 // 8MB default

	config := Config{
		NodeHome:               nodeHome,
		NodeBinary:             nodeBinary,
		Chain:                  flags.Chain,
		EnableEVM:              flags.EnableEVM,
		EnableContractMetrics:  flags.EnableContractMetrics,
		ContractMetricsLimit:   flags.ContractMetricsLimit,
		EVMBlockTypeMetrics:    flags.EVMBlockTypeMetrics,
		EnableCoreTxMetrics:    flags.EnableCoreTxMetrics,
		UseLiveState:           flags.UseLiveState,
		LiveStateCheckInterval: 5 * time.Second,
		EnableReplicaMetrics:   flags.EnableReplicaMetrics,
		ReplicaDataDir:         replicaDataDir,
		ReplicaBufferSize:      replicaBufferSize,
		EnableValidatorRTT:     false,
		MetricsAddr:            flags.MetricsAddr,
		LogLevel:               flags.LogLevel,
		LogFormat:              flags.LogFormat,
		SkipVersionCheck:       flags.SkipVersionCheck,
		SkipUpdateCheck:        flags.SkipUpdateCheck,
		ProbeInfoEndpoint:      flags.ProbeInfoEndpoint,
		InfoEndpointURL:        flags.InfoEndpointURL,
		EnableExtendedMetrics:  flags.EnableExtendedMetrics,
	}

	// override with flags if they're provided
	if flags != nil {
		if flags.NodeHome != "" {
			config.NodeHome = flags.NodeHome
		}
		if flags.NodeBinary != "" {
			config.NodeBinary = flags.NodeBinary
		}
		if flags.Chain != "" {
			config.Chain = flags.Chain
		}
		if flags.EnableContractMetrics != config.EnableContractMetrics {
			config.EnableContractMetrics = flags.EnableContractMetrics
		}
		if flags.ContractMetricsLimit != config.ContractMetricsLimit {
			config.ContractMetricsLimit = flags.ContractMetricsLimit
		}
		config.EVMBlockTypeMetrics = config.EnableEVM
		if flags.EnableCoreTxMetrics != config.EnableCoreTxMetrics {
			config.EnableCoreTxMetrics = flags.EnableCoreTxMetrics
		}
		if flags.UseLiveState != config.UseLiveState {
			config.UseLiveState = flags.UseLiveState
		}
		if flags.EnableReplicaMetrics != config.EnableReplicaMetrics {
			config.EnableReplicaMetrics = flags.EnableReplicaMetrics
		}
		if flags.EnableValidatorRTT != nil {
			config.EnableValidatorRTT = *flags.EnableValidatorRTT
		}
	}

	return config
}
