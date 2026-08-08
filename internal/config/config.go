package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	// EnablePerPeerMetrics publishes current explicit child identities from
	// child_peers status. The surface is capped at 16 identities and carries
	// no historical first/last-seen registry. Off by default.
	EnablePerPeerMetrics bool
	// TCPServicePorts is the bounded service-port vocabulary used by
	// connection and traffic monitors.
	TCPServicePorts []uint16
	// EnableTCP6 controls whether /proc/net/tcp6 participates in the atomic
	// TCP connection snapshot.
	EnableTCP6 bool
	// EnablePprof exposes Go profiling endpoints under /debug/pprof/ on
	// the metrics listener. Off by default.
	EnablePprof bool
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
	EnablePerPeerMetrics  bool
	TCPServicePorts       string
	DisableTCP6           bool
	EnablePprof           bool
}

const DefaultTCPServicePorts = "3001,3999,4001,4002,4003,4004"

// NormalizeChain returns the canonical chain name accepted by every external
// Hyperliquid endpoint. Unknown and empty values fail closed so they can never
// silently select testnet.
func NormalizeChain(raw string) (string, error) {
	chain := strings.ToLower(strings.TrimSpace(raw))
	switch chain {
	case "mainnet", "testnet":
		return chain, nil
	default:
		return "", fmt.Errorf("chain must be exactly mainnet or testnet, got %q", raw)
	}
}

// ParseTCPServicePorts parses the bounded operator-configured service-port
// vocabulary. Sorting produces one deterministic snapshot independent of CLI
// ordering.
func ParseTCPServicePorts(raw string) ([]uint16, error) {
	tokens := strings.Split(raw, ",")
	if len(tokens) == 0 || len(tokens) > 16 {
		return nil, fmt.Errorf("tcp service ports must contain 1..16 entries")
	}

	seen := make(map[uint16]struct{}, len(tokens))
	ports := make([]uint16, 0, len(tokens))
	for i, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, fmt.Errorf("tcp service port %d is empty", i+1)
		}
		for _, digit := range token {
			if digit < '0' || digit > '9' {
				return nil, fmt.Errorf("tcp service port %q is not decimal", token)
			}
		}
		value, err := strconv.ParseUint(token, 10, 16)
		if err != nil || value == 0 {
			return nil, fmt.Errorf("tcp service port %q must be in 1..65535", token)
		}
		port := uint16(value)
		if _, duplicate := seen[port]; duplicate {
			return nil, fmt.Errorf("tcp service port %d is duplicated", port)
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, nil
}

// LoadConfig loads environment variables and flags, validates configuration,
// and derives dependent paths only after all precedence rules are settled.
func LoadConfig(flags *Flags) (Config, error) {
	// load .env first
	if err := godotenv.Load(); err != nil {
		logger.Debug("No .env file found, using environment variables and flags")
	}

	homeDir := os.Getenv("HOME")
	if flags == nil {
		flags = &Flags{}
	}

	nodeHome := os.Getenv("NODE_HOME")
	if nodeHome == "" {
		nodeHome = filepath.Join(homeDir, "hl") // default fallback
	}
	if flags.NodeHome != "" {
		nodeHome = flags.NodeHome
	}

	binaryHome := os.Getenv("BINARY_HOME")
	if binaryHome == "" {
		binaryHome = homeDir
	}

	nodeBinary := os.Getenv("NODE_BINARY")
	if nodeBinary == "" {
		nodeBinary = filepath.Join(binaryHome, "hl-node")
	}
	if flags.NodeBinary != "" {
		nodeBinary = flags.NodeBinary
	}

	chain, err := NormalizeChain(flags.Chain)
	if err != nil {
		return Config{}, err
	}
	servicePorts := flags.TCPServicePorts
	if servicePorts == "" {
		servicePorts = DefaultTCPServicePorts
	}
	tcpServicePorts, err := ParseTCPServicePorts(servicePorts)
	if err != nil {
		return Config{}, err
	}

	// always use default replica data dir
	replicaDataDir := filepath.Join(nodeHome, "data", "replica_cmds")

	// always default buffer size
	replicaBufferSize := 8 // 8MB default

	config := Config{
		HomeDir:                homeDir,
		NodeHome:               nodeHome,
		BinaryHome:             binaryHome,
		NodeBinary:             nodeBinary,
		Chain:                  chain,
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
		EnablePerPeerMetrics:   flags.EnablePerPeerMetrics,
		TCPServicePorts:        tcpServicePorts,
		EnableTCP6:             !flags.DisableTCP6,
		EnablePprof:            flags.EnablePprof,
	}

	if flags.EnableValidatorRTT != nil {
		config.EnableValidatorRTT = *flags.EnableValidatorRTT
	}

	return config, nil
}
