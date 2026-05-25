package exporter

import (
	"runtime/debug"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// runMonitor launches fn in a goroutine with panic recovery and registers
// the monitor name with the metrics health tracker. On panic, the recovery
// logs the stack and increments the per-monitor panic counter.
//
// Monitors are expected to be long-running. A panic kills only that one
// goroutine; the rest of the exporter keeps running. Monitors that need
// auto-restart should manage that internally via their own loop.
func runMonitor(name string, fn func()) {
	metrics.RegisterMonitor(name)
	go func() {
		metrics.MarkMonitorStarted(name)
		defer func() {
			if r := recover(); r != nil {
				metrics.IncMonitorPanic(name)
				logger.ErrorComponent(name, "monitor PANIC recovered: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}
