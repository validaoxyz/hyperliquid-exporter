package monitors

import (
	"runtime/debug"
	"sync"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var safeWorkers sync.WaitGroup

// goSafe runs fn in a new goroutine with panic recovery. Monitors that
// spawn additional internal goroutines (e.g. consensus_monitor launches
// separate readers for consensus, status, and a housekeeping ticker) must
// use this rather than a bare `go`; otherwise the outer runMonitor wrapper
// in the exporter package only protects the top-level call, and a panic
// in any sub-goroutine kills that subsystem silently for the lifetime of
// the process.
//
// The component name is used both for the structured log entry and to
// attribute the panic in hl_exporter_monitor_panics_total{monitor=...}.
func goSafe(component string, fn func()) {
	// Register before the goroutine is scheduled so an outer setup worker
	// cannot transiently mark the logical monitor exited between spawning an
	// inner reader and returning.
	metrics.MarkMonitorStarted(component)
	safeWorkers.Add(1)
	go func() {
		defer safeWorkers.Done()
		defer metrics.MarkMonitorStopped(component)
		defer func() {
			if r := recover(); r != nil {
				metrics.IncMonitorPanic(component)
				logger.ErrorComponent(component, "subgoroutine PANIC recovered: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

// WaitForWorkers waits until every sub-goroutine started through goSafe has
// exited. Exporter shutdown calls this only after all outer monitor functions
// have returned, so no new root worker can be registered after the wait.
func WaitForWorkers() {
	safeWorkers.Wait()
}
