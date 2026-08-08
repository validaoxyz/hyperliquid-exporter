package metrics

import (
	"sort"
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
	startedUnix         int64 // atomic; latest transition from zero to one workers
	exitedUnix          int64 // atomic; latest transition from one to zero workers
	lastAttemptUnix     int64 // atomic; latest attempted source/probe cycle
	lastValidUnix       int64 // atomic; latest complete, valid observation
	lastPublicationUnix int64 // atomic; latest successful metric publication
	workers             int64 // atomic; active outer and inner monitor workers
	panics              int64 // atomic
	errors              int64 // atomic
	registered          bool
}

var (
	monitorsMu                sync.RWMutex
	monitors                  = map[string]*monitorState{}
	monitorRegistrationSealed bool
)

// BeginMonitorRegistration closes readiness while the exporter declares and
// launches its configured monitor set. Metrics HTTP may already be listening
// at this point, so readiness must not infer completeness from a partial map.
func BeginMonitorRegistration() {
	monitorsMu.Lock()
	monitorRegistrationSealed = false
	monitorsMu.Unlock()
}

// RegisterMonitor declares that a monitor exists. Required so the readyz
// endpoint knows the full set of monitors it is waiting on.
func RegisterMonitor(name string) {
	monitorsMu.Lock()
	defer monitorsMu.Unlock()
	if state, ok := monitors[name]; ok {
		state.registered = true
		return
	}
	monitors[name] = &monitorState{registered: true}
}

// SealMonitorRegistration marks the configured monitor census complete.
// Ready remains false until every registered monitor has started at least
// once; monitors that start before the seal retain that started state.
func SealMonitorRegistration() {
	monitorsMu.Lock()
	monitorRegistrationSealed = true
	monitorsMu.Unlock()
}

// MarkMonitorStarted records one active worker for a monitor. A logical
// monitor may have an outer setup worker plus multiple inner readers, so the
// running state is reference-counted rather than tied to the setup function's
// return. /readyz intentionally remains launch readiness: it is satisfied
// after the first worker starts, whether or not optional data exists yet.
func MarkMonitorStarted(name string) {
	state := getOrCreate(name)
	markMonitorStartedAt(state, time.Now().Unix())
}

func markMonitorStartedAt(state *monitorState, now int64) {
	if atomic.AddInt64(&state.workers, 1) == 1 {
		atomic.StoreInt64(&state.startedUnix, now)
		atomic.StoreInt64(&state.exitedUnix, 0)
	}
}

// MarkMonitorStopped records the end of one monitor worker. The logical
// monitor exits only when its final worker returns or panics.
func MarkMonitorStopped(name string) {
	state := getOrCreate(name)
	for {
		workers := atomic.LoadInt64(&state.workers)
		if workers == 0 {
			return
		}
		if atomic.CompareAndSwapInt64(&state.workers, workers, workers-1) {
			if workers == 1 {
				atomic.StoreInt64(&state.exitedUnix, time.Now().Unix())
			}
			return
		}
	}
}

// MarkMonitorAttempt records a real poll, read, scan, or request attempt. It
// does not imply that the input was valid or that metrics were published.
func MarkMonitorAttempt(name string) {
	state := getOrCreate(name)
	markMonitorAttemptAt(state, time.Now().Unix())
}

func markMonitorAttemptAt(state *monitorState, now int64) {
	atomic.StoreInt64(&state.lastAttemptUnix, now)
}

// MarkMonitorValidObservation records a complete observation that passed the
// source's parse/schema checks. It does not imply publication.
func MarkMonitorValidObservation(name string) {
	state := getOrCreate(name)
	markMonitorValidObservationAt(state, time.Now().Unix())
}

func markMonitorValidObservationAt(state *monitorState, now int64) {
	atomic.StoreInt64(&state.lastAttemptUnix, now)
	atomic.StoreInt64(&state.lastValidUnix, now)
}

// MarkMonitorPublication records a successful publication of a complete
// observation.
func MarkMonitorPublication(name string) {
	state := getOrCreate(name)
	markMonitorPublicationAt(state, time.Now().Unix())
}

func markMonitorPublicationAt(state *monitorState, now int64) {
	atomic.StoreInt64(&state.lastPublicationUnix, now)
}

// MarkMonitorTick is the compatibility helper used by existing monitors after
// a successful processing cycle. New or corrected monitors should use the
// attempt/valid/publication functions at their exact boundaries.
func MarkMonitorTick(name string) {
	state := getOrCreate(name)
	now := time.Now().Unix()
	atomic.StoreInt64(&state.lastAttemptUnix, now)
	atomic.StoreInt64(&state.lastValidUnix, now)
	atomic.StoreInt64(&state.lastPublicationUnix, now)
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

// IncMonitorErrorDrop records a report that could not enter one monitor's
// independent cap-1 error channel. The caller is responsible for reducing
// the monitor label to the fixed exporter vocabulary.
func IncMonitorErrorDrop(name string) {
	HLExporterMonitorErrorDropsTotal.WithLabelValues(name).Inc()
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

// Ready reports whether every registered monitor has started at least once.
// We deliberately do NOT require each one to have produced a real tick —
// many monitors legitimately sit idle on non-validator nodes (no consensus
// dir) or when a feature is disabled. Running/exited and source freshness are
// exported separately.
func Ready() bool {
	monitorsMu.RLock()
	defer monitorsMu.RUnlock()
	if !monitorRegistrationSealed || len(monitors) == 0 {
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
	Name                string
	Registered          bool
	Running             bool
	Workers             int64
	StartedUnix         int64
	ExitedUnix          int64
	LastAttemptUnix     int64
	LastValidUnix       int64
	LastPublicationUnix int64
	Panics              int64
	Errors              int64
}

func snapshotMonitors() []MonitorSnapshot {
	monitorsMu.RLock()
	defer monitorsMu.RUnlock()
	out := make([]MonitorSnapshot, 0, len(monitors))
	for name, state := range monitors {
		workers := atomic.LoadInt64(&state.workers)
		out = append(out, MonitorSnapshot{
			Name:                name,
			Registered:          state.registered,
			Running:             workers > 0,
			Workers:             workers,
			StartedUnix:         atomic.LoadInt64(&state.startedUnix),
			ExitedUnix:          atomic.LoadInt64(&state.exitedUnix),
			LastAttemptUnix:     atomic.LoadInt64(&state.lastAttemptUnix),
			LastValidUnix:       atomic.LoadInt64(&state.lastValidUnix),
			LastPublicationUnix: atomic.LoadInt64(&state.lastPublicationUnix),
			Panics:              atomic.LoadInt64(&state.panics),
			Errors:              atomic.LoadInt64(&state.errors),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
