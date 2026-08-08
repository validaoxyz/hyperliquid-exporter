package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

const scrapeTimeout = 30 * time.Second

func StartPrometheusServer(ctx context.Context, port int, enablePprof bool) error {
	mux := http.NewServeMux()

	// http.TimeoutHandler buffers the response and writes a 503 if the inner
	// handler exceeds the deadline. Unlike a hand-rolled goroutine+select, it
	// never races with the inner handler on the ResponseWriter — which was the
	// source of the "superfluous response.WriteHeader" log spam.
	metricsHandler := http.TimeoutHandler(
		promhttp.Handler(),
		scrapeTimeout,
		"Metrics collection timed out\n",
	)
	mux.Handle("/metrics", metricsHandler)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK\n"))
	})

	// /livez always 200s while the process is running. /readyz reports startup
	// readiness once every configured monitor has launched at least one worker;
	// source availability and freshness are separate metrics.
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if Ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
	})

	if enablePprof {
		// profiling endpoints, opt-in via --pprof: the next CPU question
		// should take a `go tool pprof` minute, not a code audit
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		logger.Info("pprof endpoints enabled on /debug/pprof/")
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("Starting Prometheus metrics server on port %d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// an exporter that cannot serve metrics is worse than a dead
			// one: it looks alive while every scrape fails (seen live with
			// a port collision). Die loudly so systemd restarts us and the
			// failure is visible.
			logger.Error("Prometheus server failed (exiting): %v", err)
			os.Exit(1)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("Error shutting down Prometheus server: %v", err)
		}
	}()

	return nil
}
