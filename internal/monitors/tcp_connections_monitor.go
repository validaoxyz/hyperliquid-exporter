package monitors

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// tcpConnectionsPollInterval — /proc/net/tcp{,6} are small and cheap to
// read; polling at 15 s gives near-real-time visibility into peer churn
// without measurable cost.
const tcpConnectionsPollInterval = 15 * time.Second

// tcpListenPorts is the fixed set of hl-node listening ports we report
// state counts for. Confirmed via `ss -tlnp` on a live mainnet peer:
//
//	4001 — gossip
//	4002 — gossip secondary
//	3999 — --serve-info
//	3001 — --serve-evm-rpc
//
// We only report state counts for connections WHERE OUR SIDE IS ONE OF
// THESE LISTENING PORTS, so the per-port label has bounded cardinality.
var tcpListenPorts = []uint16{4001, 4002, 3999, 3001}

// tcpStateNames maps the hex state codes used by /proc/net/tcp to the
// well-known names. See net/tcp_states.h in the kernel.
var tcpStateNames = map[uint8]string{
	0x01: "ESTABLISHED",
	0x02: "SYN_SENT",
	0x03: "SYN_RECV",
	0x04: "FIN_WAIT1",
	0x05: "FIN_WAIT2",
	0x06: "TIME_WAIT",
	0x07: "CLOSE",
	0x08: "CLOSE_WAIT",
	0x09: "LAST_ACK",
	0x0A: "LISTEN",
	0x0B: "CLOSING",
}

// StartTCPConnectionsMonitor reports the count of TCP connections on
// each hl-node listening port, grouped by socket state. Linux-only;
// no-ops on other OSes through the platform shim file.
//
// Operator signal: the "right-now" peer count from ESTABLISHED on 4001
// is a much truer view than the ~260-peer file snapshot from
// tcp_traffic_monitor. Sustained TIME_WAIT growth on 4002 (we observed
// 33 on a live node) indicates peer churn invisible from any other
// source.
func StartTCPConnectionsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	if !procFSAvailable() {
		logger.InfoComponent("tcp_connections",
			"tcp_connections monitor disabled: /proc not available on this OS")
		<-ctx.Done()
		return
	}

	logger.InfoComponent("tcp_connections", "watching /proc/net/tcp{,6}")

	ticker := time.NewTicker(tcpConnectionsPollInterval)
	defer ticker.Stop()

	tickTCPConnections()
	metrics.MarkMonitorTick("tcp_connections")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickTCPConnections()
			metrics.MarkMonitorTick("tcp_connections")
		}
	}
}
