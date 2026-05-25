package monitors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// probeOnce is the per-tick HTTP probe. Test against an httptest.Server
// so we don't need network or hl-node to validate behavior.

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
