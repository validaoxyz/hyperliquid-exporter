package metrics

import (
	"context"
	"runtime"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"go.opentelemetry.io/otel/metric"
)

var (
	// memory metrics
	HLGoHeapObjects   metric.Int64ObservableGauge
	HLGoHeapInuseMB   metric.Float64ObservableGauge
	HLGoHeapIdleMB    metric.Float64ObservableGauge
	HLGoSysMB         metric.Float64ObservableGauge
	HLGoNumGoroutines metric.Int64ObservableGauge
)

func InitMemoryMetrics(meter metric.Meter) error {
	var err error

	HLGoHeapObjects, err = meter.Int64ObservableGauge(
		"hl_go_heap_objects",
		metric.WithDescription("Number of allocated heap objects"),
	)
	if err != nil {
		return err
	}

	HLGoHeapInuseMB, err = meter.Float64ObservableGauge(
		"hl_go_heap_inuse_mb",
		metric.WithDescription("Heap memory in use in MB"),
	)
	if err != nil {
		return err
	}

	HLGoHeapIdleMB, err = meter.Float64ObservableGauge(
		"hl_go_heap_idle_mb",
		metric.WithDescription("Heap memory idle in MB"),
	)
	if err != nil {
		return err
	}

	HLGoSysMB, err = meter.Float64ObservableGauge(
		"hl_go_sys_mb",
		metric.WithDescription("Total memory obtained from OS in MB"),
	)
	if err != nil {
		return err
	}

	HLGoNumGoroutines, err = meter.Int64ObservableGauge(
		"hl_go_num_goroutines",
		metric.WithDescription("Number of goroutines"),
	)
	if err != nil {
		return err
	}

	// callback registration is now handled in RegisterCallbacks() in callbacks.go
	// to avoid conflicts with the main metrics callback
	return nil
}

// callback function that collects memory stats
func collectMemoryStats(ctx context.Context, observer metric.Observer) error {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// record heap objects
	observer.ObserveInt64(HLGoHeapObjects, int64(m.HeapObjects))

	// convert bytes->MB for readability
	observer.ObserveFloat64(HLGoHeapInuseMB, float64(m.HeapInuse)/(1024*1024))
	observer.ObserveFloat64(HLGoHeapIdleMB, float64(m.HeapIdle)/(1024*1024))
	observer.ObserveFloat64(HLGoSysMB, float64(m.Sys)/(1024*1024))

	// record number of goroutines
	observer.ObserveInt64(HLGoNumGoroutines, int64(runtime.NumGoroutine()))

	return nil
}

// starts periodic memory monitoring
func StartMemoryMonitoring(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	lastGoroutineCount := runtime.NumGoroutine()
	goroutineGrowthWarnings := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			currentGoroutines := runtime.NumGoroutine()

			// log if memory usage is high
			heapInuseMB := float64(m.HeapInuse) / (1024 * 1024)
			if heapInuseMB > 10240 { // log if over 10GB
				logger.WarningComponent("memory", "High memory usage detected: HeapInuse=%.2fMB, HeapObjects=%d, Goroutines=%d",
					heapInuseMB, m.HeapObjects, currentGoroutines)
			}

			// detect goroutine leaks, only log errors for extreme growth
			if currentGoroutines > lastGoroutineCount+10 {
				goroutineGrowthWarnings++
				// only log debug message for moderate growth
				logger.DebugComponent("memory", "Goroutine count growing: %d -> %d (growth warnings: %d)",
					lastGoroutineCount, currentGoroutines, goroutineGrowthWarnings)

				// only error if we've seen consistent growth (10+ warnings)
				if goroutineGrowthWarnings >= 10 {
					logger.ErrorComponent("memory", "Potential goroutine leak detected! Count has grown from initial to %d", currentGoroutines)
				}
			} else if currentGoroutines < lastGoroutineCount {
				// reset warnings if goroutines decreased
				goroutineGrowthWarnings = 0
			}

			lastGoroutineCount = currentGoroutines
		}
	}
}
