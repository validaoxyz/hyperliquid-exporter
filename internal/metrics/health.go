package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Build-time injected version information. Set via -ldflags.
var (
	BuildVersion   = "dev"
	BuildCommit    = "unknown"
	BuildGoVersion = ""
)

// monitorState tracks the runtime state of one monitor.
type monitorState struct {
	startedUnix  int64 // atomic; set once when the monitor goroutine launches
	lastTickUnix int64 // atomic; updated on every real processing cycle
	panics       int64 // atomic
	errors       int64 // atomic
	registered   bool
}

var (
	monitorsMu sync.RWMutex
	monitors   = map[string]*monitorState{}
)

// RegisterMonitor declares that a monitor exists. Required so the readyz
// endpoint knows the full set of monitors it is waiting on.
func RegisterMonitor(name string) {
	monitorsMu.Lock()
	defer monitorsMu.Unlock()
	if _, ok := monitors[name]; !ok {
		monitors[name] = &monitorState{registered: true}
	}
}

// MarkMonitorStarted records the goroutine-launch time for a monitor.
// /readyz uses this — a monitor counts as ready as soon as its goroutine
// is running, whether or not it has data to process yet. Operators
// use last_tick_seconds to detect actual stalls.
func MarkMonitorStarted(name string) {
	state := getOrCreate(name)
	atomic.StoreInt64(&state.startedUnix, time.Now().Unix())
}

// MarkMonitorTick records that the monitor produced a successful processing
// cycle. The readyz endpoint flips to ready once every registered monitor has
// reported at least one tick.
func MarkMonitorTick(name string) {
	state := getOrCreate(name)
	atomic.StoreInt64(&state.lastTickUnix, time.Now().Unix())
}

// IncMonitorPanic increments the recovered-panic counter for a monitor.
// The internal atomic backs the MonitorSnapshot health API; the
// hl_exporter_monitor_panics_total Counter is driven directly here.
func IncMonitorPanic(name string) {
	state := getOrCreate(name)
	atomic.AddInt64(&state.panics, 1)
	HLExporterMonitorPanicsTotal.WithLabelValues(name).Inc()
}

// IncMonitorError increments the soft-error counter for a monitor. Use this
// for parse / IO failures that the monitor recovers from on the next loop.
// The internal atomic backs the MonitorSnapshot health API; the
// hl_exporter_monitor_errors_total Counter is driven directly here.
func IncMonitorError(name string) {
	state := getOrCreate(name)
	atomic.AddInt64(&state.errors, 1)
	HLExporterMonitorErrorsTotal.WithLabelValues(name).Inc()
}

func getOrCreate(name string) *monitorState {
	monitorsMu.RLock()
	state, ok := monitors[name]
	monitorsMu.RUnlock()
	if ok {
		return state
	}
	monitorsMu.Lock()
	defer monitorsMu.Unlock()
	if state, ok = monitors[name]; ok {
		return state
	}
	state = &monitorState{}
	monitors[name] = state
	return state
}

// Ready reports whether every registered monitor's goroutine has started.
// We deliberately do NOT require each one to have produced a real tick —
// many monitors legitimately sit idle on non-validator nodes (no consensus
// dir) or when a feature is disabled. Operators alert on stalled monitors
// via time() - hl_exporter_monitor_last_tick_seconds, not /readyz.
func Ready() bool {
	monitorsMu.RLock()
	defer monitorsMu.RUnlock()
	if len(monitors) == 0 {
		return false
	}
	for _, state := range monitors {
		if !state.registered {
			continue
		}
		if atomic.LoadInt64(&state.startedUnix) == 0 {
			return false
		}
	}
	return true
}

// MonitorSnapshot returns a copy of the current health state. Callbacks use
// this when emitting the observable gauges.
type MonitorSnapshot struct {
	Name         string
	StartedUnix  int64
	LastTickUnix int64
	Panics       int64
	Errors       int64
}

func snapshotMonitors() []MonitorSnapshot {
	monitorsMu.RLock()
	defer monitorsMu.RUnlock()
	out := make([]MonitorSnapshot, 0, len(monitors))
	for name, state := range monitors {
		out = append(out, MonitorSnapshot{
			Name:         name,
			StartedUnix:  atomic.LoadInt64(&state.startedUnix),
			LastTickUnix: atomic.LoadInt64(&state.lastTickUnix),
			Panics:       atomic.LoadInt64(&state.panics),
			Errors:       atomic.LoadInt64(&state.errors),
		})
	}
	return out
}
