package monitors

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// tcpConnectionsPollInterval — /proc/net/tcp{,6} are small and cheap to
// read; polling at 15 s gives near-real-time visibility into peer churn
// without measurable cost.
const tcpConnectionsPollInterval = 15 * time.Second

// tcpListenPorts is the default bounded service-port vocabulary. Operators may
// replace it through Config.TCPServicePorts (1..16 unique nonzero ports):
//
//	3001 — --serve-info
//	3999 — allowlisted EVM-RPC bridge
//	4001,4002 — gossip services
//	4003,4004 — observed numeric services; no stable role is claimed for 4004
var tcpListenPorts = []uint16{3001, 3999, 4001, 4002, 4003, 4004}

const maxTCPServicePorts = 16

func validateTCPServicePorts(ports []uint16) ([]uint16, error) {
	if len(ports) == 0 || len(ports) > maxTCPServicePorts {
		return nil, fmt.Errorf("tcp service ports must contain 1..%d entries", maxTCPServicePorts)
	}
	seen := make(map[uint16]struct{}, len(ports))
	out := append([]uint16(nil), ports...)
	for _, port := range out {
		if port == 0 {
			return nil, fmt.Errorf("tcp service port must be non-zero")
		}
		if _, exists := seen[port]; exists {
			return nil, fmt.Errorf("duplicate tcp service port %d", port)
		}
		seen[port] = struct{}{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func tcpServicePortsForConfig(cfg config.Config) ([]uint16, error) {
	ports := cfg.TCPServicePorts
	if len(ports) == 0 {
		ports = tcpListenPorts
	}
	return validateTCPServicePorts(ports)
}

// StartTCPConnectionsMonitor reports socket rows associated with each tracked
// service port on exactly one literal socket side, grouped by state. Linux-only;
// no-ops on other OSes through the platform shim file.
//
// Operator signal: ESTABLISHED rows show current socket load on each service,
// not a peer count (one endpoint may own several sockets). Sustained TIME_WAIT
// growth is a bounded churn symptom; it does not identify topology or cause.
func StartTCPConnectionsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	if !procFSAvailable() {
		metrics.RegisterSource(metrics.SourceTCPConnections, false)
		logger.InfoComponent("tcp_connections",
			"tcp_connections monitor disabled: /proc not available on this OS")
		<-ctx.Done()
		return
	}
	metrics.RegisterSource(metrics.SourceTCPConnections, true)
	ports, err := tcpServicePortsForConfig(cfg)
	if err != nil {
		metrics.MarkMonitorAttempt("tcp_connections")
		metrics.MarkSourceError(metrics.SourceTCPConnections, metrics.SourceFailureSchema)
		metrics.IncMonitorError("tcp_connections")
		logger.ErrorComponent("tcp_connections", "invalid service-port configuration: %v", err)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("tcp_connections", "watching /proc/net/tcp{,6}")

	ticker := time.NewTicker(tcpConnectionsPollInterval)
	defer ticker.Stop()

	tickTCPConnections(ports, cfg.EnableTCP6)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickTCPConnections(ports, cfg.EnableTCP6)
		}
	}
}
