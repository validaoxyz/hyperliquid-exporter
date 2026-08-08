package metrics

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

type blockingGatherer struct {
	started chan struct{}
	release chan struct{}
}

func (g *blockingGatherer) Gather() ([]*dto.MetricFamily, error) {
	g.started <- struct{}{}
	<-g.release
	return nil, nil
}

func TestPrometheusServerTimeouts(t *testing.T) {
	server := newPrometheusServer(8086, http.NewServeMux())
	if server.ReadHeaderTimeout != 5*time.Second || server.WriteTimeout != 35*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatalf("timeouts = read-header:%v write:%v idle:%v", server.ReadHeaderTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestMetricsConcurrencyCapDoesNotBlockHealth(t *testing.T) {
	gatherer := &blockingGatherer{
		started: make(chan struct{}, maxScrapesInFlight),
		release: make(chan struct{}),
	}
	mux := newPrometheusMux(false, gatherer)

	var workers sync.WaitGroup
	workers.Add(maxScrapesInFlight)
	for range maxScrapesInFlight {
		go func() {
			defer workers.Done()
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		}()
	}
	for range maxScrapesInFlight {
		select {
		case <-gatherer.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for blocked scrape")
		}
	}

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	sixth := httptest.NewRecorder()
	mux.ServeHTTP(sixth, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if sixth.Code != http.StatusServiceUnavailable {
		t.Fatalf("sixth scrape status = %d, want %d", sixth.Code, http.StatusServiceUnavailable)
	}

	close(gatherer.release)
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked scrapes did not exit")
	}
}
