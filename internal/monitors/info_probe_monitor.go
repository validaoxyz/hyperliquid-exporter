package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	infoProbeInterval    = 15 * time.Second
	infoProbeHTTPTimeout = 5 * time.Second
	defaultInfoEndpoint  = "http://127.0.0.1:3001/info"
	infoProbeRequestBody = `{"type":"meta"}`
)

var (
	infoMetaLastSuccessNanos     atomic.Int64
	infoExchangeLastSuccessNanos atomic.Int64
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

	// series exist only when the probe is enabled (absent != 0)
	metrics.InitInfoProbeInstruments()
	metrics.InitInfoProbeStatusInstruments()
	metrics.RegisterSource(metrics.SourceInfoMeta, true)
	metrics.RegisterSource(metrics.SourceInfoExchange, true)

	client := &http.Client{Timeout: infoProbeHTTPTimeout}
	ticker := time.NewTicker(infoProbeInterval)
	defer ticker.Stop()

	probeOnce(ctx, client, url)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeOnce(ctx, client, url)
		}
	}
}

func probeOnce(ctx context.Context, client *http.Client, url string) bool {
	return probeOnceAt(ctx, client, url, time.Now)
}

func probeOnceAt(ctx context.Context, client *http.Client, url string, now func() time.Time) bool {
	metrics.MarkMonitorAttempt("info_probe")
	metrics.InitInfoProbeStatusInstruments()
	metaOK := probeMeta(ctx, client, url, now)
	exchangeOK := probeExchangeStatus(ctx, client, url, now)
	if metaOK || exchangeOK {
		metrics.MarkMonitorValidObservation("info_probe")
		metrics.MarkMonitorPublication("info_probe")
		return true
	}
	return false
}

func probeMeta(ctx context.Context, client *http.Client, url string, now func() time.Time) bool {
	metrics.MarkSourceAttempt(metrics.SourceInfoMeta)
	start := now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(infoProbeRequestBody))
	if err != nil {
		recordMetaFailure(metrics.InfoProbeOutcomeBuild, metrics.SourceFailureRequest, now())
		logger.DebugComponent("info_probe", "build request: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	latency := now().Sub(start)
	if latency < 0 {
		latency = 0
	}
	metrics.HLInfoEndpointLatencySeconds.Observe(latency.Seconds())

	if err != nil {
		recordMetaFailure(metrics.InfoProbeOutcomeRequest, metrics.SourceFailureRequest, now())
		logger.DebugComponent("info_probe", "POST %s: %v", url, err)
		return false
	}
	defer resp.Body.Close()

	// Read a bounded amount of the body to confirm non-emptiness — but
	// don't drain the whole response, the /info endpoint returns the
	// full perp universe and that's wasted bytes for a health check.
	buf := make([]byte, 64)
	n, _ := io.ReadFull(resp.Body, buf)
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK && n > 0 {
		successAt := now()
		metrics.HLInfoEndpointUp.Set(1)
		metrics.HLInfoEndpointLastSuccessSeconds.Set(float64(successAt.Unix()))
		metrics.HLInfoMetaLastSuccessAgeSeconds.Set(0)
		metrics.HLInfoMetaOutcomesTotal.WithLabelValues(metrics.InfoProbeOutcomeSuccess).Inc()
		infoMetaLastSuccessNanos.Store(successAt.UnixNano())
		metrics.MarkSourceValidObservation(metrics.SourceInfoMeta, time.Time{})
		metrics.MarkSourcePublication(metrics.SourceInfoMeta)
		return true
	}

	outcome := metrics.InfoProbeOutcomeEmpty
	stage := metrics.SourceFailureSchema
	if resp.StatusCode != http.StatusOK {
		outcome = metrics.InfoProbeOutcomeStatus
		stage = metrics.SourceFailureStatus
	}
	recordMetaFailure(outcome, stage, now())
	logger.DebugComponent("info_probe", "POST %s -> %d body_len=%d", url, resp.StatusCode, n)
	return false
}

// probeExchangeStatus asks the node's own info endpoint for exchangeStatus
// and publishes local_wall_clock - exchange_time. The node README's
// recommended health check is exactly this comparison: a node can serve
// 200s while its exchange state has stopped advancing.
func probeExchangeStatus(ctx context.Context, client *http.Client, url string, now func() time.Time) bool {
	metrics.MarkSourceAttempt(metrics.SourceInfoExchange)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(`{"type":"exchangeStatus"}`))
	if err != nil {
		recordExchangeFailure(metrics.InfoProbeOutcomeBuild, metrics.SourceFailureRequest, now())
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		recordExchangeFailure(metrics.InfoProbeOutcomeRequest, metrics.SourceFailureRequest, now())
		logger.DebugComponent("info_probe", "exchangeStatus: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		recordExchangeFailure(metrics.InfoProbeOutcomeStatus, metrics.SourceFailureStatus, now())
		return false
	}

	var body struct {
		Time float64 `json:"time"` // milliseconds since epoch
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.Time <= 0 {
		recordExchangeFailure(metrics.InfoProbeOutcomeDecode, metrics.SourceFailureDecode, now())
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	successAt := now()
	metrics.InitExchangeStatusDeltaInstrument()
	metrics.HLInfoExchangeStatusDeltaSeconds.Set(float64(successAt.UnixMilli())/1000.0 - body.Time/1000.0)
	metrics.HLInfoExchangeStatusUp.Set(1)
	metrics.HLInfoExchangeStatusLastSuccess.Set(float64(successAt.Unix()))
	metrics.HLInfoExchangeStatusLastSuccessAge.Set(0)
	metrics.HLInfoExchangeStatusOutcomesTotal.WithLabelValues(metrics.InfoProbeOutcomeSuccess).Inc()
	infoExchangeLastSuccessNanos.Store(successAt.UnixNano())
	metrics.MarkSourceValidObservation(metrics.SourceInfoExchange, time.Time{})
	metrics.MarkSourcePublication(metrics.SourceInfoExchange)
	return true
}

func recordMetaFailure(outcome string, stage metrics.SourceFailureStage, now time.Time) {
	metrics.HLInfoEndpointUp.Set(0)
	metrics.HLInfoEndpointFailuresTotal.Inc()
	metrics.HLInfoMetaOutcomesTotal.WithLabelValues(outcome).Inc()
	updateProbeAge(metrics.HLInfoMetaLastSuccessAgeSeconds, infoMetaLastSuccessNanos.Load(), now)
	metrics.MarkSourceError(metrics.SourceInfoMeta, stage)
}

func recordExchangeFailure(outcome string, stage metrics.SourceFailureStage, now time.Time) {
	metrics.HLInfoExchangeStatusUp.Set(0)
	metrics.HLInfoExchangeStatusOutcomesTotal.WithLabelValues(outcome).Inc()
	updateProbeAge(metrics.HLInfoExchangeStatusLastSuccessAge, infoExchangeLastSuccessNanos.Load(), now)
	metrics.MarkSourceError(metrics.SourceInfoExchange, stage)
}

func updateProbeAge(gauge interface{ Set(float64) }, lastSuccessNanos int64, now time.Time) {
	if lastSuccessNanos == 0 {
		return
	}
	age := now.Sub(time.Unix(0, lastSuccessNanos)).Seconds()
	if age < 0 {
		age = 0
	}
	gauge.Set(age)
}
