package monitors

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// parentPeerPollInterval matches the tcp_traffic cadence; there's nothing
// new to look at until the source monitor publishes a fresh snapshot.
const parentPeerPollInterval = 30 * time.Second

// parentPeerAmbiguityThreshold is the fraction of the top peer's bytes the
// runner-up must exceed before we log a warning about an ambiguous parent.
// 0.1 = "runner-up is within 10× ratio of the top". A healthy parent
// relationship has a clear winner (often 100x the runner-up); ambiguity
// usually means the node has lost its primary or is bridging two.
const parentPeerAmbiguityThreshold = 0.1

// StartParentPeerMonitor identifies which inbound peer is delivering the
// most bytes — for a non-validator, that's the node's effective upstream
// data source ("parent peer"). The metric set lets operators alert on
// rapid parent switches (potential connectivity issues) and on parent
// peer loss.
//
// Inspired by dwellir-public/hyperliquid-exporter's parent_peer_monitor.
// Our implementation reuses the shared sample from tcp_traffic_monitor
// rather than reading tcp_traffic itself a second time.
func StartParentPeerMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	logger.InfoComponent("parent_peer", "starting parent peer monitor (depends on tcp_traffic sample)")

	var currentParent string
	var parentSince time.Time

	ticker := time.NewTicker(parentPeerPollInterval)
	defer ticker.Stop()

	tick := func() {
		ts, in, _ := LatestTCPTrafficSnapshot()
		if ts.IsZero() || len(in) == 0 {
			return
		}

		top := in[0]
		var runner peerSample
		if len(in) > 1 {
			runner = in[1]
		}

		if top.value > 0 && runner.value > top.value*parentPeerAmbiguityThreshold {
			logger.WarningComponent("parent_peer",
				"ambiguous parent peer: top=%s (%.4g) runner-up=%s (%.4g)",
				top.ip, top.value, runner.ip, runner.value)
		}

		now := time.Now()
		if top.ip != currentParent {
			if currentParent != "" {
				metrics.HLNodeParentPeerInfo.DeleteLabelValues(currentParent)
				metrics.HLNodeParentPeerSwitches.Inc()
				logger.InfoComponent("parent_peer", "parent peer changed: %s -> %s", currentParent, top.ip)
			} else {
				logger.InfoComponent("parent_peer", "initial parent peer: %s", top.ip)
			}
			currentParent = top.ip
			parentSince = now
		}

		metrics.HLNodeParentPeerInfo.WithLabelValues(top.ip).Set(1)
		metrics.HLNodeParentPeerTraffic.Set(top.value)
		metrics.HLNodeParentPeerTenureSeconds.Set(now.Sub(parentSince).Seconds())
		metrics.MarkMonitorTick("parent_peer")
	}

	// Wait briefly for the tcp_traffic monitor to publish its first sample
	// before we tick — otherwise the first iteration is wasted on an empty
	// snapshot.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}

	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
