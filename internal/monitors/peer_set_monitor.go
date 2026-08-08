package monitors

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/peerset"
)

const peerSetPollInterval = 30 * time.Second

var (
	lastTrafficEndpointsAdded   uint64
	lastTrafficEndpointsEvicted = map[string]uint64{"ttl": 0, "capacity": 0}
)

// StartPeerSetMonitor publishes only bounded aggregate observation-set state.
// The former 2x2048 raw-IP first/last-seen surface is intentionally no longer
// populated, including when the compatibility --per-peer-metrics flag is set.
func StartPeerSetMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	logger.InfoComponent("peer_set", "publishing process-local qualified traffic-endpoint aggregates")
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	ticker := time.NewTicker(peerSetPollInterval)
	defer ticker.Stop()
	tickPeerSet(cfg.EnablePerPeerMetrics)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickPeerSet(cfg.EnablePerPeerMetrics)
		}
	}
}

func tickPeerSet(_ bool) {
	metrics.MarkMonitorAttempt("peer_set")
	metrics.WithPrometheusSnapshotUpdate(func() {
		set := peerset.Default()
		now := time.Now()
		set.EvictExpired(now)

		for _, window := range []struct {
			label string
			age   time.Duration
		}{{"5m", 5 * time.Minute}, {"1h", time.Hour}, {"24h", 24 * time.Hour}} {
			value := float64(set.UniqueSeenIn(window.age, now))
			metrics.HLP2PTrafficEndpointsSeen.WithLabelValues(window.label).Set(value)
			metrics.HLP2PUniquePeersSeen.WithLabelValues(window.label).Set(value) // one-release alias
		}

		added := set.AddedTotal()
		if added >= lastTrafficEndpointsAdded {
			delta := float64(added - lastTrafficEndpointsAdded)
			metrics.HLP2PTrafficEndpointsAddedTotal.Add(delta)
			metrics.HLP2PPeersAddedTotal.Add(delta) // one-release alias
		}
		lastTrafficEndpointsAdded = added

		var totalEvictionDelta uint64
		for _, reason := range []string{"ttl", "capacity"} {
			current := set.EvictedByReason(reason)
			previous := lastTrafficEndpointsEvicted[reason]
			if current >= previous {
				delta := current - previous
				metrics.HLP2PTrafficEndpointsEvictedTotal.WithLabelValues(reason).Add(float64(delta))
				totalEvictionDelta += delta
			}
			lastTrafficEndpointsEvicted[reason] = current
		}
		metrics.HLP2PPeersEvictedTotal.Add(float64(totalEvictionDelta)) // one-release alias

		metrics.MarkMonitorValidObservation("peer_set")
		metrics.MarkMonitorPublication("peer_set")
	})
}
