package monitors

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const childStderrPollInterval = 60 * time.Second

// childStderrHeadBytes is a hard per-file read bound. A larger artifact is
// explicitly reported as truncated; classifications from its prefix remain
// bounded evidence rather than a claim about the unseen suffix.
const childStderrHeadBytes = 4096

const (
	childStderrStateEmpty      = "empty"
	childStderrStateReadable   = "readable"
	childStderrStateTruncated  = "truncated"
	childStderrStateUnreadable = "unreadable"

	childStderrReasonNone    = "none"
	childStderrReasonUnknown = "unknown"
	childStderrReasonPanic   = "panic"
)

// childCrashReasons is a finite evidence taxonomy checked before generic
// panic signatures. These signatures come from retained validator artifacts;
// they are not a version-independent list of every possible child exit.
var childCrashReasons = []struct {
	reason  string
	needles [][]byte
}{
	{"app_hash_mismatch", [][]byte{[]byte("computed app hash"), []byte("does not match quorum")}},
	{"hardfork_upgrade", [][]byte{[]byte("observed qc for newer hardfork")}},
	{"sync_overflow", [][]byte{[]byte("too many blocks to request")}},
	{"config_error", [][]byte{[]byte("is not in node_ips"), []byte("could not read"), []byte("configuration error")}},
	{"network", [][]byte{[]byte("connection timeout"), []byte("upstream connect error"), []byte("invalid ip address")}},
}

var explicitPanicNeedles = [][]byte{
	[]byte("panicked at"),
	[]byte("panicked:"),
}

func childStderrReasons() []string {
	reasons := make([]string, 0, len(childCrashReasons)+3)
	reasons = append(reasons, childStderrReasonNone)
	for _, class := range childCrashReasons {
		reasons = append(reasons, class.reason)
	}
	reasons = append(reasons, childStderrReasonPanic, childStderrReasonUnknown)
	return reasons
}

// classifyChildStderr maps a bounded stderr prefix to a finite reason. An
// unmatched message is unknown; panic is reserved for explicit panic text.
func classifyChildStderr(head []byte) string {
	head = bytes.ToLower(head)
	for _, class := range childCrashReasons {
		for _, needle := range class.needles {
			if bytes.Contains(head, needle) {
				return class.reason
			}
		}
	}
	for _, needle := range explicitPanicNeedles {
		if bytes.Contains(head, needle) {
			return childStderrReasonPanic
		}
	}
	return childStderrReasonUnknown
}

type childStderrState struct {
	state   string
	reason  string
	size    int64
	modTime time.Time
}

type childStderrSeries struct {
	state  string
	reason string
}

func childStderrSeriesCensus() []childStderrSeries {
	reasons := childStderrReasons()
	series := []childStderrSeries{
		{state: childStderrStateEmpty, reason: childStderrReasonNone},
		{state: childStderrStateUnreadable, reason: childStderrReasonNone},
	}
	for _, state := range []string{childStderrStateReadable, childStderrStateTruncated} {
		for _, reason := range reasons[1:] { // known classes, explicit panic, unknown
			series = append(series, childStderrSeries{state: state, reason: reason})
		}
	}
	return series
}

// StartChildStderrMonitor watches $NODE_HOME/data/visor_child_stderr, where
// hl-visor retains one artifact per child start. Date directory names are a
// node-internal schedule and may be future-dated, so recency uses only mtimes.
func StartChildStderrMonitor(ctx context.Context, cfg config.Config) {
	root := filepath.Join(cfg.NodeHome, "data", "visor_child_stderr")
	metrics.RegisterSource(metrics.SourceChildStderr, true)

	logger.InfoComponent("child_stderr", "watching %s", root)
	seen := map[string]*childStderrState{}

	ticker := time.NewTicker(childStderrPollInterval)
	defer ticker.Stop()

	tickChildStderr(root, seen)
	metrics.MarkMonitorTick("child_stderr")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickChildStderr(root, seen)
			metrics.MarkMonitorTick("child_stderr")
		}
	}
}

func tickChildStderr(root string, seen map[string]*childStderrState) {
	tickChildStderrWith(root, seen, scanChildStderr, time.Now)
}

type childStderrScanFunc func(string, map[string]*childStderrState) (map[string]*childStderrState, error)

func tickChildStderrWith(root string, seen map[string]*childStderrState, scan childStderrScanFunc, now func() time.Time) bool {
	metrics.MarkMonitorAttempt("child_stderr")
	metrics.MarkSourceAttempt(metrics.SourceChildStderr)
	next, err := scan(root, seen)
	if err != nil {
		metrics.HLNodeChildStderrScanUp.Set(0)
		if errors.Is(err, fs.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceChildStderr)
		} else {
			metrics.MarkSourceError(metrics.SourceChildStderr, metrics.SourceFailureRead)
		}
		logger.DebugComponent("child_stderr", "directory scan incomplete; retaining last complete snapshot: %v", err)
		return false
	}

	for path := range seen {
		delete(seen, path)
	}
	for path, state := range next {
		seen[path] = state
	}
	publishChildStderrSnapshot(seen)
	completedAt := now()
	metrics.HLNodeChildStderrScanUp.Set(1)
	metrics.HLNodeChildStderrLastCompleteTimestampSeconds.Set(float64(completedAt.Unix()))
	metrics.MarkSourceValidObservation(metrics.SourceChildStderr, time.Time{})
	metrics.MarkSourcePublication(metrics.SourceChildStderr)
	metrics.MarkMonitorValidObservation("child_stderr")
	metrics.MarkMonitorPublication("child_stderr")
	return true
}

type childReadDirFunc func(string) ([]os.DirEntry, error)
type childReadHeadFunc func(string, int) ([]byte, error)

func scanChildStderr(root string, previous map[string]*childStderrState) (map[string]*childStderrState, error) {
	return scanChildStderrWith(root, previous, os.ReadDir, readHead)
}

// scanChildStderrWith stages a complete retained-file snapshot. Directory or
// metadata failure rejects the scan; a per-file content read failure is an
// explicit artifact state and therefore remains publishable.
func scanChildStderrWith(
	root string,
	previous map[string]*childStderrState,
	readDir childReadDirFunc,
	read childReadHeadFunc,
) (map[string]*childStderrState, error) {
	next := make(map[string]*childStderrState)
	dateDirs, err := readDir(root)
	if err != nil {
		return nil, err
	}
	for _, dateDir := range dateDirs {
		if !dateDir.IsDir() {
			continue
		}
		datePath := filepath.Join(root, dateDir.Name())
		nDirs, err := readDir(datePath)
		if err != nil {
			return nil, err
		}
		for _, nDir := range nDirs {
			if !nDir.IsDir() {
				continue
			}
			dir := filepath.Join(datePath, nDir.Name())
			files, err := readDir(dir)
			if err != nil {
				return nil, err
			}
			for _, file := range files {
				if file.IsDir() {
					continue
				}
				info, err := file.Info()
				if err != nil {
					return nil, err
				}
				path := filepath.Join(dir, file.Name())
				if prior := previous[path]; prior != nil && prior.state != childStderrStateUnreadable && prior.size == info.Size() && prior.modTime.Equal(info.ModTime()) {
					copy := *prior
					next[path] = &copy
					continue
				}

				state := &childStderrState{
					size:    info.Size(),
					modTime: info.ModTime(),
				}
				if info.Size() == 0 {
					state.state = childStderrStateEmpty
					state.reason = childStderrReasonNone
					next[path] = state
					continue
				}

				head, err := read(path, childStderrHeadBytes)
				if err != nil {
					state.state = childStderrStateUnreadable
					state.reason = childStderrReasonNone
				} else {
					state.state = childStderrStateReadable
					if info.Size() > childStderrHeadBytes {
						state.state = childStderrStateTruncated
					}
					state.reason = classifyChildStderr(head)
				}
				next[path] = state
			}
		}
	}
	return next, nil
}

func publishChildStderrSnapshot(seen map[string]*childStderrState) {
	reasons := childStderrReasons()
	counts := make(map[string]int)
	newest := make(map[string]time.Time)
	legacyCounts := make(map[string]int)
	legacyNewest := make(map[string]time.Time)

	for _, state := range seen {
		key := state.state + "\x00" + state.reason
		counts[key]++
		if state.modTime.After(newest[key]) {
			newest[key] = state.modTime
		}
		if state.state != childStderrStateReadable && state.state != childStderrStateTruncated {
			continue
		}
		if state.reason == childStderrReasonUnknown || state.reason == childStderrReasonNone {
			continue
		}
		legacyCounts[state.reason]++
		if state.modTime.After(legacyNewest[state.reason]) {
			legacyNewest[state.reason] = state.modTime
		}
	}

	metrics.HLNodeChildStarts.Set(float64(len(seen)))
	for _, series := range childStderrSeriesCensus() {
		key := series.state + "\x00" + series.reason
		metrics.HLNodeChildStderrArtifacts.WithLabelValues(series.state, series.reason).Set(float64(counts[key]))
		metrics.HLNodeChildStderrLastArtifactTimestampSeconds.DeleteLabelValues(series.state, series.reason)
		if timestamp := newest[key]; !timestamp.IsZero() {
			metrics.HLNodeChildStderrLastArtifactTimestampSeconds.WithLabelValues(series.state, series.reason).Set(float64(timestamp.Unix()))
		}
	}
	for _, reason := range reasons[1 : len(reasons)-1] { // known classes plus explicit panic; exclude none and unknown
		metrics.HLNodeChildCrashes.WithLabelValues(reason).Set(float64(legacyCounts[reason]))
		metrics.HLNodeChildLastCrashSeconds.WithLabelValues(reason).Set(0)
		if timestamp := legacyNewest[reason]; !timestamp.IsZero() {
			metrics.HLNodeChildLastCrashSeconds.WithLabelValues(reason).Set(float64(timestamp.Unix()))
		}
	}
}

func readHead(path string, n int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, int64(n)))
}
