package monitors

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	infoProbeInterval     = 15 * time.Second
	infoProbeHTTPTimeout  = 5 * time.Second
	defaultInfoEndpoint   = "http://127.0.0.1:3001/info"
	infoProbeRequestBody  = `{"type":"meta"}`
)

// StartInfoProbeMonitor actively POSTs `{"type":"meta"}` to the node's
// --serve-info endpoint every infoProbeInterval. A 200 with a non-empty
// body counts as up; anything else is down and increments the failure
// counter.
//
// This is the only health probe we have that doesn't depend on file
// mtimes — those can be confused by clock skew or a node that's still
// alive but stuck writing. A live HTTP response from the node's own RPC
// stack is much harder to fake.
func StartInfoProbeMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	url := cfg.InfoEndpointURL
	if url == "" {
		url = defaultInfoEndpoint
	}

	logger.InfoComponent("info_probe", "probing %s every %s", url, infoProbeInterval)

	client := &http.Client{Timeout: infoProbeHTTPTimeout}
	ticker := time.NewTicker(infoProbeInterval)
	defer ticker.Stop()

	probeOnce(ctx, client, url)
	metrics.MarkMonitorTick("info_probe")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeOnce(ctx, client, url)
			metrics.MarkMonitorTick("info_probe")
		}
	}
}

func probeOnce(ctx context.Context, client *http.Client, url string) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(infoProbeRequestBody))
	if err != nil {
		metrics.HLInfoEndpointUp.Set(0)
		metrics.HLInfoEndpointFailuresTotal.Inc()
		logger.DebugComponent("info_probe", "build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	latency := time.Since(start)
	metrics.HLInfoEndpointLatencySeconds.Observe(latency.Seconds())

	if err != nil {
		metrics.HLInfoEndpointUp.Set(0)
		metrics.HLInfoEndpointFailuresTotal.Inc()
		logger.DebugComponent("info_probe", "POST %s: %v", url, err)
		return
	}
	defer resp.Body.Close()

	// Read a bounded amount of the body to confirm non-emptiness — but
	// don't drain the whole response, the /info endpoint returns the
	// full perp universe and that's wasted bytes for a health check.
	buf := make([]byte, 64)
	n, _ := io.ReadFull(resp.Body, buf)
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK && n > 0 {
		metrics.HLInfoEndpointUp.Set(1)
		metrics.HLInfoEndpointLastSuccessSeconds.Set(float64(time.Now().Unix()))
		return
	}

	metrics.HLInfoEndpointUp.Set(0)
	metrics.HLInfoEndpointFailuresTotal.Inc()
	logger.DebugComponent("info_probe", "POST %s -> %d body_len=%d", url, resp.StatusCode, n)
}
