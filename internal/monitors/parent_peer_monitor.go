package monitors

import (
	"context"
	"sort"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	dominantInboundPollInterval = 30 * time.Second
	dominantInboundEWMAAlpha    = 0.3
	dominantInboundSwitchRatio  = 1.2
	dominantInboundPruneBelow   = 1e-9
	dominantInboundMaxAge       = 90 * time.Second
)

type dominantInboundState struct {
	current       string
	selectedAt    time.Time
	ewma          map[string]float64
	lastProcessed time.Time
}

func newDominantInboundState() *dominantInboundState {
	return &dominantInboundState{ewma: make(map[string]float64)}
}

// StartDominantInboundMonitor derives one bounded traffic heuristic. It makes
// no parent, relay, reachability, quality, or sync attribution.
func StartDominantInboundMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	logger.InfoComponent("dominant_inbound", "starting dominant inbound traffic endpoint monitor")
	state := newDominantInboundState()
	ticker := time.NewTicker(dominantInboundPollInterval)
	defer ticker.Stop()

	tickDominantInbound(time.Now(), state)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickDominantInbound(time.Now(), state)
		}
	}
}

func tickDominantInbound(now time.Time, state *dominantInboundState) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		tickDominantInboundGeneration(now, state)
	})
}

func tickDominantInboundGeneration(now time.Time, state *dominantInboundState) {
	metrics.MarkMonitorAttempt("dominant_inbound")
	_, receivedAt, inbound, _ := LatestTCPTrafficObservation()
	if receivedAt.IsZero() || now.Sub(receivedAt) > dominantInboundMaxAge {
		clearDominantInbound(state)
		return
	}
	metrics.HLP2PDominantInboundFresh.Set(1)
	if receivedAt.Equal(state.lastProcessed) {
		publishDominantInbound(now, nil, state)
		return
	}
	state.lastProcessed = receivedAt

	hasPositive := false
	for _, sample := range inbound {
		if sample.value > 0 {
			hasPositive = true
			break
		}
	}
	if !hasPositive {
		clearDominantInbound(state)
		// A complete empty/all-zero observation is fresh source truth even
		// though it intentionally has no dominant candidate.
		metrics.HLP2PDominantInboundFresh.Set(1)
		state.lastProcessed = receivedAt
		metrics.MarkMonitorValidObservation("dominant_inbound")
		metrics.MarkMonitorPublication("dominant_inbound")
		return
	}

	latest := make(map[string]float64, len(inbound))
	seen := make(map[string]struct{}, len(inbound))
	for _, sample := range inbound {
		seen[sample.ip] = struct{}{}
		latest[sample.ip] = sample.value
		state.ewma[sample.ip] = dominantInboundEWMAAlpha*sample.value + (1-dominantInboundEWMAAlpha)*state.ewma[sample.ip]
	}
	for ip, value := range state.ewma {
		if _, ok := seen[ip]; ok {
			continue
		}
		value *= 1 - dominantInboundEWMAAlpha
		if value < dominantInboundPruneBelow {
			delete(state.ewma, ip)
		} else {
			state.ewma[ip] = value
		}
	}

	ranked := rankInboundEWMA(state.ewma)
	if len(ranked) == 0 || ranked[0].value <= 0 {
		clearDominantInbound(state)
		metrics.HLP2PDominantInboundFresh.Set(1)
		state.lastProcessed = receivedAt
		return
	}
	best := ranked[0]
	if state.current == "" {
		state.current = best.ip
		state.selectedAt = now
	} else if best.ip != state.current && best.value > state.ewma[state.current]*dominantInboundSwitchRatio {
		metrics.HLP2PDominantInboundEndpointInfo.DeleteLabelValues(state.current)
		state.current = best.ip
		state.selectedAt = now
		metrics.HLP2PDominantInboundSwitchesTotal.Inc()
	}

	publishDominantInbound(now, latest, state)
	metrics.MarkMonitorValidObservation("dominant_inbound")
	metrics.MarkMonitorPublication("dominant_inbound")
}

type rankedEndpoint struct {
	ip    string
	value float64
}

func rankInboundEWMA(values map[string]float64) []rankedEndpoint {
	out := make([]rankedEndpoint, 0, len(values))
	for ip, value := range values {
		if value > 0 {
			out = append(out, rankedEndpoint{ip: ip, value: value})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].value != out[j].value {
			return out[i].value > out[j].value
		}
		return out[i].ip < out[j].ip
	})
	return out
}

func publishDominantInbound(now time.Time, latest map[string]float64, state *dominantInboundState) {
	if state.current == "" {
		return
	}
	metrics.HLP2PDominantInboundEndpointInfo.WithLabelValues(state.current).Set(1)
	if latest != nil {
		metrics.HLP2PDominantInboundLatestValue.Set(latest[state.current])
	}
	selected := state.ewma[state.current]
	metrics.HLP2PDominantInboundEWMAValue.Set(selected)
	metrics.HLP2PDominantInboundTenureSeconds.Set(nonnegativeDuration(now.Sub(state.selectedAt)).Seconds())

	ranked := rankInboundEWMA(state.ewma)
	var total, challenger float64
	ties := 0
	best := 0.0
	if len(ranked) > 0 {
		best = ranked[0].value
	}
	for _, endpoint := range ranked {
		total += endpoint.value
		if endpoint.ip != state.current && endpoint.value > challenger {
			challenger = endpoint.value
		}
		if endpoint.value == best {
			ties++
		}
	}
	share := 0.0
	if total > 0 {
		share = selected / total
	}
	challengerRatio := 0.0
	if selected > 0 && challenger > 0 {
		challengerRatio = challenger / selected
	}
	metrics.HLP2PDominantInboundShareRatio.Set(share)
	metrics.HLP2PDominantInboundChallengerRatio.Set(challengerRatio)
	metrics.HLP2PDominantInboundTieCount.Set(float64(ties))
}

func clearDominantInbound(state *dominantInboundState) {
	if state.current != "" {
		metrics.HLP2PDominantInboundEndpointInfo.DeleteLabelValues(state.current)
	}
	state.current = ""
	state.selectedAt = time.Time{}
	clear(state.ewma)
	metrics.HLP2PDominantInboundLatestValue.Set(0)
	metrics.HLP2PDominantInboundEWMAValue.Set(0)
	metrics.HLP2PDominantInboundTenureSeconds.Set(0)
	metrics.HLP2PDominantInboundShareRatio.Set(0)
	metrics.HLP2PDominantInboundChallengerRatio.Set(0)
	metrics.HLP2PDominantInboundTieCount.Set(0)
	metrics.HLP2PDominantInboundFresh.Set(0)
}
