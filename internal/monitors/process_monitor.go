package monitors

import (
	"context"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// processNames are the hl- processes we look up via /proc. Both run on
// validator and non-validator nodes; on container deployments either may
// be absent. The monitor publishes hl_node_process_up=0 for missing ones
// so operators can alert on "expected hl-node process not running".
var processNames = []string{"hl-node", "hl-visor"}

const (
	processIOReadBytes     = "read_bytes"
	processIOWriteBytes    = "write_bytes"
	processIOReadSyscalls  = "read_syscalls"
	processIOWriteSyscalls = "write_syscalls"
)

var processIOOperations = []string{
	processIOReadBytes,
	processIOWriteBytes,
	processIOReadSyscalls,
	processIOWriteSyscalls,
}

// processPollInterval is short because operators want quick detection of
// hl-node crashes — restart-loop monitoring is one of the canonical
// reasons to run this exporter.
const processPollInterval = 15 * time.Second

// StartProcessMonitor publishes resource usage and liveness for each
// known hl- process. The implementation is Linux-only; on darwin/macOS
// (or any OS without /proc) the monitor logs once and idles.
func StartProcessMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	enabled := procFSAvailable()
	metrics.RegisterSource(metrics.SourceProcess, enabled)
	if !enabled {
		logger.InfoComponent("process",
			"process monitor disabled: /proc not available on this OS")
		<-ctx.Done()
		return
	}

	logger.InfoComponent("process", "watching %v via /proc", processNames)

	ticker := time.NewTicker(processPollInterval)
	defer ticker.Stop()
	state := newProcessMonitorState()

	tickProcesses(state)
	metrics.MarkMonitorTick("process")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickProcesses(state)
			metrics.MarkMonitorTick("process")
		}
	}
}

func tickProcesses(state *processMonitorState) bool {
	return tickProcessesWith(state, findProcesses)
}

type processScanFunc func([]string) (map[string]processSelection, error)

func tickProcessesWith(state *processMonitorState, scan processScanFunc) bool {
	metrics.MarkMonitorAttempt("process")
	metrics.MarkSourceAttempt(metrics.SourceProcess)
	selections, err := scan(processNames)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceProcess, metrics.SourceFailureRead)
		metrics.IncMonitorError("process")
		logger.DebugComponent("process", "procfs scan incomplete; retaining last complete snapshot: %v", err)
		return false
	}

	for _, name := range processNames {
		selection := selections[name]
		metrics.HLNodeProcessEligibleMatches.WithLabelValues(name).Set(float64(selection.Eligible))
		for _, operation := range processIOOperations {
			metrics.HLNodeProcessIOTotal.WithLabelValues(name, operation).Add(0)
		}
		if !selection.Found {
			publishMissingProcess(name)
			state.reset(name)
			continue
		}

		info := selection.Info
		metrics.HLNodeProcessUp.WithLabelValues(name).Set(1)
		metrics.HLNodeProcessStartTimeSeconds.WithLabelValues(name).Set(float64(info.StartTimeUnix))
		metrics.HLNodeProcessCPUSecondsTotal.WithLabelValues(name).Set(info.CPUSeconds)
		metrics.HLNodeProcessRSSBytes.WithLabelValues(name).Set(float64(info.RSSBytes))
		metrics.HLNodeProcessVirtBytes.WithLabelValues(name).Set(float64(info.VirtBytes))
		metrics.HLNodeProcessThreads.WithLabelValues(name).Set(float64(info.Threads))
		metrics.HLNodeProcessOpenFDs.WithLabelValues(name).Set(float64(info.OpenFDs))
		metrics.HLNodeProcessMaxFDs.WithLabelValues(name).Set(float64(info.MaxFDs))
		ratio := 0.0
		if info.MaxFDs > 0 {
			ratio = float64(info.OpenFDs) / float64(info.MaxFDs)
		}
		metrics.HLNodeProcessOpenFDsRatio.WithLabelValues(name).Set(ratio)

		if delta, ok := state.observe(name, info); ok {
			metrics.HLNodeProcessIOTotal.WithLabelValues(name, processIOReadBytes).Add(float64(delta.ReadBytes))
			metrics.HLNodeProcessIOTotal.WithLabelValues(name, processIOWriteBytes).Add(float64(delta.WriteBytes))
			metrics.HLNodeProcessIOTotal.WithLabelValues(name, processIOReadSyscalls).Add(float64(delta.ReadSyscalls))
			metrics.HLNodeProcessIOTotal.WithLabelValues(name, processIOWriteSyscalls).Add(float64(delta.WriteSyscalls))
		}
	}

	metrics.MarkSourceValidObservation(metrics.SourceProcess, time.Time{})
	metrics.MarkSourcePublication(metrics.SourceProcess)
	metrics.MarkMonitorValidObservation("process")
	metrics.MarkMonitorPublication("process")
	return true
}

func publishMissingProcess(name string) {
	metrics.HLNodeProcessUp.WithLabelValues(name).Set(0)
	// A complete procfs scan proved the process absent. Zero current-value
	// gauges so dashboards do not present the last live process as current;
	// exporter-lifetime IO counters deliberately remain monotonic.
	metrics.HLNodeProcessStartTimeSeconds.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessCPUSecondsTotal.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessRSSBytes.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessVirtBytes.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessThreads.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessOpenFDs.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessMaxFDs.WithLabelValues(name).Set(0)
	metrics.HLNodeProcessOpenFDsRatio.WithLabelValues(name).Set(0)
}

// processInfo is the OS-independent view the publisher consumes.
type processInfo struct {
	PID            int
	StartTimeTicks uint64
	StartTimeUnix  int64
	CPUSeconds     float64
	RSSBytes       int64
	VirtBytes      int64
	Threads        int64
	OpenFDs        int64
	MaxFDs         uint64
	IO             processIOValues
	IOValid        bool
}

type processSelection struct {
	Info     processInfo
	Eligible int
	Found    bool
}

type processIOValues struct {
	ReadBytes     uint64
	WriteBytes    uint64
	ReadSyscalls  uint64
	WriteSyscalls uint64
}

type processEpoch struct {
	PID            int
	StartTimeTicks uint64
}

type processIOBaseline struct {
	epoch processEpoch
	value processIOValues
}

type processMonitorState struct {
	io map[string]processIOBaseline
}

func newProcessMonitorState() *processMonitorState {
	return &processMonitorState{io: make(map[string]processIOBaseline, len(processNames))}
}

func (s *processMonitorState) reset(name string) {
	delete(s.io, name)
}

// observe returns positive deltas only within one exact Linux process epoch.
// A new epoch establishes a baseline. A field that rolls backwards is
// independently rebased, so one procfs reset cannot produce a negative value
// or suppress unrelated operations until the old raw value is reached again.
func (s *processMonitorState) observe(name string, info processInfo) (processIOValues, bool) {
	if !info.IOValid {
		return processIOValues{}, false
	}
	epoch := processEpoch{PID: info.PID, StartTimeTicks: info.StartTimeTicks}
	previous, exists := s.io[name]
	s.io[name] = processIOBaseline{epoch: epoch, value: info.IO}
	if !exists || previous.epoch != epoch {
		return processIOValues{}, true
	}
	return positiveProcessIODelta(previous.value, info.IO), true
}

func positiveProcessIODelta(previous, current processIOValues) processIOValues {
	return processIOValues{
		ReadBytes:     positiveUint64Delta(previous.ReadBytes, current.ReadBytes),
		WriteBytes:    positiveUint64Delta(previous.WriteBytes, current.WriteBytes),
		ReadSyscalls:  positiveUint64Delta(previous.ReadSyscalls, current.ReadSyscalls),
		WriteSyscalls: positiveUint64Delta(previous.WriteSyscalls, current.WriteSyscalls),
	}
}

func positiveUint64Delta(previous, current uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}
