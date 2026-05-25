package monitors

import (
	"context"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/peerset"
)

// peerSetPollInterval is how often we evict TTL-expired peers and
// refresh the windowed gauges. The set is small (≤2048) so this is
// cheap; running every 30s keeps the windowed counts current without
// thrashing.
const peerSetPollInterval = 30 * time.Second

// perPeerLabels remembers which IPs we've published per-peer gauges
// for, so we can Delete() series when an IP is evicted from the set.
// Only consulted when cfg.EnablePerPeerMetrics is true.
var (
	perPeerLabelsMu sync.Mutex
	perPeerLabels   = map[string]struct{}{}
)

// Last-observed peerset add/evict totals, so we can drive the
// hl_p2p_peers_added_total / hl_p2p_peers_evicted_total Counters by
// .Add(delta) each tick. The peerset exposes monotonic running totals;
// Counters cannot be .Set(), so we advance them by the per-tick delta.
// Only touched from the single peer_set goroutine, so no lock needed.
var (
	lastPeersAdded   uint64
	lastPeersEvicted uint64
)

// StartPeerSetMonitor publishes the windowed unique-peer counts always,
// and (when --per-peer-metrics is set) the per-IP last-seen + first-seen
// gauges. The set itself is fed from other monitors (tcp_traffic for
// now); this monitor is the publisher, not the producer.
func StartPeerSetMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	logger.InfoComponent("peer_set", "publishing peer-set metrics; per-peer-metrics=%v", cfg.EnablePerPeerMetrics)

	// Delay the first tick so tcp_traffic's first registration has time
	// to land. Without this, peer_set's t=0 tick races tcp_traffic's t=0
	// tick and frequently wins, displaying 0 peers for the first 30s
	// after startup even though the producer has work queued.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	ticker := time.NewTicker(peerSetPollInterval)
	defer ticker.Stop()

	tickPeerSet(cfg.EnablePerPeerMetrics)
	metrics.MarkMonitorTick("peer_set")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickPeerSet(cfg.EnablePerPeerMetrics)
			metrics.MarkMonitorTick("peer_set")
		}
	}
}

func tickPeerSet(emitPerPeer bool) {
	set := peerset.Default()
	now := time.Now()

	// Evict TTL-expired peers and drop their per-IP series if any.
	evicted := set.EvictExpired(now)
	if emitPerPeer && len(evicted) > 0 {
		perPeerLabelsMu.Lock()
		for _, ip := range evicted {
			if _, ok := perPeerLabels[ip]; ok {
				metrics.HLP2PPeerLastSeenSeconds.DeleteLabelValues(ip)
				metrics.HLP2PPeerFirstSeenSeconds.DeleteLabelValues(ip)
				delete(perPeerLabels, ip)
			}
		}
		perPeerLabelsMu.Unlock()
	}

	// Windowed counts.
	metrics.HLP2PUniquePeersSeen.WithLabelValues("5m").Set(float64(set.UniqueSeenIn(5*time.Minute, now)))
	metrics.HLP2PUniquePeersSeen.WithLabelValues("1h").Set(float64(set.UniqueSeenIn(time.Hour, now)))
	metrics.HLP2PUniquePeersSeen.WithLabelValues("24h").Set(float64(set.UniqueSeenIn(24*time.Hour, now)))

	// Aggregate counters. hl_p2p_peers is set by tcp_traffic_monitor
	// (latest-sample count), not from set.Len(). The added/evicted totals
	// are real Counters: advance them by the delta since the last tick
	// (clamp negatives to 0 in case the peerset's internal totals reset).
	added := set.AddedTotal()
	if added >= lastPeersAdded {
		metrics.HLP2PPeersAddedTotal.Add(float64(added - lastPeersAdded))
	}
	lastPeersAdded = added

	evictedTotal := set.EvictedTotal()
	if evictedTotal >= lastPeersEvicted {
		metrics.HLP2PPeersEvictedTotal.Add(float64(evictedTotal - lastPeersEvicted))
	}
	lastPeersEvicted = evictedTotal

	if !emitPerPeer {
		return
	}

	perPeerLabelsMu.Lock()
	defer perPeerLabelsMu.Unlock()
	for _, p := range set.Snapshot() {
		metrics.HLP2PPeerLastSeenSeconds.WithLabelValues(p.IP).Set(float64(p.LastSeen.Unix()))
		metrics.HLP2PPeerFirstSeenSeconds.WithLabelValues(p.IP).Set(float64(p.FirstSeen.Unix()))
		perPeerLabels[p.IP] = struct{}{}
	}
}
