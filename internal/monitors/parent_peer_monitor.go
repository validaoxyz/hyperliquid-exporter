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

// Per-peer traffic values oscillate wildly between 30s windows (a peer can
// drop from ~1.0 to ~1e-6 just by not delivering a block that window), so
// parent selection runs on an EWMA of the samples rather than the latest
// one, and the incumbent only loses to a challenger that is clearly ahead
// on the smoothed value. This kills both the flapping switches and the
// once-per-30s "ambiguous parent" log spam of the single-sample design.
const (
	// parentPeerEWMAAlpha weights the newest sample; ~0.3 makes the EWMA
	// settle over roughly the last 5 samples (2.5 minutes).
	parentPeerEWMAAlpha = 0.3
	// parentPeerSwitchRatio is how far ahead (on EWMA) a challenger must be
	// before it takes the parent role from the incumbent.
	parentPeerSwitchRatio = 1.2
	// parentPeerPruneBelow drops decayed-out peers from the EWMA map.
	parentPeerPruneBelow = 1e-9
)

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
	ewma := map[string]float64{}
	var lastSample time.Time

	ticker := time.NewTicker(parentPeerPollInterval)
	defer ticker.Stop()

	tick := func() {
		// mark liveness before the emptiness gate: an empty snapshot is a
		// healthy outcome on an idle node, not a dead monitor
		metrics.MarkMonitorTick("parent_peer")

		ts, in, _ := LatestTCPTrafficSnapshot()
		if ts.IsZero() || len(in) == 0 || !ts.After(lastSample) {
			return
		}
		lastSample = ts

		// fold the fresh snapshot into the EWMA; peers absent from the
		// snapshot decay toward zero and eventually drop out
		seen := make(map[string]bool, len(in))
		latest := make(map[string]float64, len(in))
		for _, p := range in {
			seen[p.ip] = true
			latest[p.ip] = p.value
			ewma[p.ip] = parentPeerEWMAAlpha*p.value + (1-parentPeerEWMAAlpha)*ewma[p.ip]
		}
		for ip, v := range ewma {
			if !seen[ip] {
				v *= 1 - parentPeerEWMAAlpha
				if v < parentPeerPruneBelow {
					delete(ewma, ip)
					continue
				}
				ewma[ip] = v
			}
		}

		var best string
		var bestVal float64
		for ip, v := range ewma {
			if v > bestVal {
				best, bestVal = ip, v
			}
		}
		if best == "" {
			return
		}

		now := time.Now()
		switch {
		case currentParent == "":
			currentParent = best
			parentSince = now
			logger.InfoComponent("parent_peer", "initial parent peer: %s", best)
		case best != currentParent:
			// hysteresis: the incumbent keeps the role until the challenger
			// is clearly ahead on the smoothed value
			if bestVal > ewma[currentParent]*parentPeerSwitchRatio {
				metrics.HLNodeParentPeerInfo.DeleteLabelValues(currentParent)
				metrics.HLNodeParentPeerSwitches.Inc()
				logger.InfoComponent("parent_peer", "parent peer changed: %s -> %s", currentParent, best)
				currentParent = best
				parentSince = now
			}
		}

		metrics.HLNodeParentPeerInfo.WithLabelValues(currentParent).Set(1)
		metrics.HLNodeParentPeerTraffic.Set(latest[currentParent])
		metrics.HLNodeParentPeerTenureSeconds.Set(now.Sub(parentSince).Seconds())
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
