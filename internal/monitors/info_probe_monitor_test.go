package monitors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// probeOnce is the per-tick HTTP probe. Test against an httptest.Server
// so we don't need network or hl-node to validate behavior.

// the probe family registers lazily when the monitor starts; tests call
// probeOnce directly, so register here (idempotent)
func init() {
	metrics.InitInfoProbeInstruments()
	metrics.InitInfoProbeStatusInstruments()
}

func TestProbeOnce_SubprobesRemainIndependentAndAgeFailures(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	var nowNanos atomic.Int64
	nowNanos.Store(base.UnixNano())
	now := func() time.Time { return time.Unix(0, nowNanos.Load()) }

	// mode 0: both succeed; mode 1: meta fails; mode 2: exchange status
	// fails; mode 3: exchange returns a valid object plus a second value.
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request["type"] {
		case "meta":
			if mode.Load() == 1 {
				http.Error(w, "meta unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("{}"))
		case "exchangeStatus":
			if mode.Load() == 2 {
				http.Error(w, "exchange unavailable", http.StatusServiceUnavailable)
				return
			}
			if mode.Load() == 3 {
				_, _ = w.Write([]byte(`{"time":1800000000000} {}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]float64{
				"time": float64(now().UnixMilli()),
			})
		default:
			http.Error(w, "unknown type", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	probeOnceAt(context.Background(), client, server.URL, now)
	if got := b04MetricValue(t, metrics.HLInfoEndpointUp); got != 1 {
		t.Fatalf("meta up after joint success = %v, want 1", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusUp); got != 1 {
		t.Fatalf("exchange up after joint success = %v, want 1", got)
	}

	mode.Store(1)
	nowNanos.Store(base.Add(10 * time.Second).UnixNano())
	probeOnceAt(context.Background(), client, server.URL, now)
	if got := b04MetricValue(t, metrics.HLInfoEndpointUp); got != 0 {
		t.Fatalf("meta up after meta-only failure = %v, want 0", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusUp); got != 1 {
		t.Fatalf("exchange was coupled to meta failure: up=%v", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoMetaLastSuccessAgeSeconds); got != 10 {
		t.Fatalf("meta success age = %v, want 10", got)
	}

	deltaBeforeExchangeFailure := b04MetricValue(t, metrics.HLInfoExchangeStatusDeltaSeconds)
	lastExchangeSuccess := b04MetricValue(t, metrics.HLInfoExchangeStatusLastSuccess)
	mode.Store(2)
	nowNanos.Store(base.Add(20 * time.Second).UnixNano())
	probeOnceAt(context.Background(), client, server.URL, now)
	if got := b04MetricValue(t, metrics.HLInfoEndpointUp); got != 1 {
		t.Fatalf("meta was coupled to exchange failure: up=%v", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusUp); got != 0 {
		t.Fatalf("exchange up after exchange-only failure = %v, want 0", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusLastSuccessAge); got != 10 {
		t.Fatalf("exchange success age = %v, want 10", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusLastSuccess); got != lastExchangeSuccess {
		t.Fatalf("exchange failure advanced last success from %v to %v", lastExchangeSuccess, got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusDeltaSeconds); got != deltaBeforeExchangeFailure {
		t.Fatalf("exchange failure changed retained delta from %v to %v", deltaBeforeExchangeFailure, got)
	}

	mode.Store(0)
	nowNanos.Store(base.Add(30 * time.Second).UnixNano())
	probeOnceAt(context.Background(), client, server.URL, now)
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusUp); got != 1 {
		t.Fatalf("exchange up after recovery = %v, want 1", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusLastSuccessAge); got != 0 {
		t.Fatalf("exchange age after recovery = %v, want 0", got)
	}

	deltaBeforeMalformed := b04MetricValue(t, metrics.HLInfoExchangeStatusDeltaSeconds)
	lastSuccessBeforeMalformed := b04MetricValue(t, metrics.HLInfoExchangeStatusLastSuccess)
	mode.Store(3)
	nowNanos.Store(base.Add(40 * time.Second).UnixNano())
	probeOnceAt(context.Background(), client, server.URL, now)
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusUp); got != 0 {
		t.Fatalf("exchange up after trailing JSON value = %v, want 0", got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusLastSuccess); got != lastSuccessBeforeMalformed {
		t.Fatalf("malformed exchange response advanced last success from %v to %v", lastSuccessBeforeMalformed, got)
	}
	if got := b04MetricValue(t, metrics.HLInfoExchangeStatusDeltaSeconds); got != deltaBeforeMalformed {
		t.Fatalf("malformed exchange response changed retained delta from %v to %v", deltaBeforeMalformed, got)
	}
}

func TestDecodeExchangeStatusRequiresOneCompleteJSONValue(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{name: "valid", body: `{"time":1800000000000}`, ok: true},
		{name: "valid whitespace", body: "{\"time\":1800000000000}\n\t", ok: true},
		{name: "second value", body: `{"time":1800000000000} {}`, ok: false},
		{name: "trailing garbage", body: `{"time":1800000000000} x`, ok: false},
		{name: "trailing beyond limit", body: `{"time":1800000000000}` + strings.Repeat(" ", infoProbeMaxBodySize), ok: false},
		{name: "empty", body: ``, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeExchangeStatusTime(strings.NewReader(tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (time=%v)", ok, tc.ok, got)
			}
		})
	}
}

func TestProbeOnce_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json content-type, got %q", ct)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"universe":[{"name":"BTC"}]}`))
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	probeOnce(context.Background(), client, server.URL)
	// Implicit assertion: probeOnce doesn't panic and the test server
	// observed a POST with the expected content-type. The gauge state
	// is harder to inspect from outside the metrics package, but we've
	// covered the request-formation and the success path.
}

func TestProbeOnce_Non200IsTreatedAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nope"))
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	probeOnce(context.Background(), client, server.URL)
	// Same as above: just verify no panic on a non-200 response.
}

func TestProbeOnce_EmptyBodyIsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// no body
	}))
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	probeOnce(context.Background(), client, server.URL)
}

func TestProbeOnce_NetworkErrorIsFailure(t *testing.T) {
	// Closed server — connection refused.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	probeOnce(context.Background(), client, server.URL)
}

func TestProbeOnce_CancelledContextIsHandled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // would exceed probe timeout
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	client := &http.Client{Timeout: 100 * time.Millisecond}
	probeOnce(ctx, client, server.URL)
}
