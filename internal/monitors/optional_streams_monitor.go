package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	optionalStreamsPollInterval = 60 * time.Second
	optionalStreamRecordLimit   = int64(64 << 20)
	optionalStreamScanChunk     = 64 << 10
	optionalStreamScanLimit     = optionalStreamRecordLimit + optionalStreamScanChunk
)

// optionalStreamDirs is the allowlist of opt-in hl-node data streams whose
// freshness we track. Operators enable these with node flags
// (--write-fills, --write-misc-events, --write-system-and-core-writer-actions,
// TWAP streaming) specifically to feed downstream consumers; the stream
// content is bulk order-flow/ledger data and out of metric scope, but a
// stream that silently stops writing breaks those consumers, and nothing
// else surfaces that.
var optionalStreamDirs = []struct {
	name   string
	source metrics.SourceID
}{
	{"node_fills_streaming", metrics.SourceOptionalFills},
	{"node_twap_statuses_streaming", metrics.SourceOptionalTWAP},
	{"misc_events", metrics.SourceOptionalMisc},
	{"system_and_core_writer_actions", metrics.SourceOptionalEvents},
}

type optionalStreamState struct {
	lastValid time.Time
}

// StartOptionalStreamsMonitor publishes the age of the newest file in each
// present opt-in stream under $NODE_HOME/data/<stream>/hourly/. Streams
// whose directory is absent produce no series.
func StartOptionalStreamsMonitor(ctx context.Context, cfg config.Config) {
	dataRoot := filepath.Join(cfg.NodeHome, "data")
	states := make(map[string]*optionalStreamState, len(optionalStreamDirs))

	present := []string{}
	for _, stream := range optionalStreamDirs {
		metrics.RegisterSource(stream.source, true)
		states[stream.name] = &optionalStreamState{}
		if _, err := os.Stat(filepath.Join(dataRoot, stream.name)); err == nil {
			present = append(present, stream.name)
		}
	}
	// keep ticking even when none are present: the tick is four stats a
	// minute and a stream can appear when the operator flips a node flag
	logger.InfoComponent("optional_streams", "watching opt-in streams (present now: %v)", present)

	ticker := time.NewTicker(optionalStreamsPollInterval)
	defer ticker.Stop()

	tickOptionalStreams(dataRoot, states, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickOptionalStreams(dataRoot, states, time.Now())
		}
	}
}

func tickOptionalStreams(dataRoot string, states map[string]*optionalStreamState, now time.Time) {
	metrics.MarkMonitorAttempt("optional_streams")
	for _, stream := range optionalStreamDirs {
		state := states[stream.name]
		metrics.MarkSourceAttempt(stream.source)
		root := filepath.Join(dataRoot, stream.name, "hourly")
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				metrics.HLNodeStreamAgeSeconds.DeleteLabelValues(stream.name)
				state.lastValid = time.Time{}
				metrics.MarkSourceAbsent(stream.source)
			} else {
				metrics.MarkSourceError(stream.source, metrics.SourceFailureStat)
			}
			continue
		}
		path, err := latestHourlyFile(root)
		if err != nil {
			if os.IsNotExist(err) {
				metrics.HLNodeStreamAgeSeconds.DeleteLabelValues(stream.name)
				state.lastValid = time.Time{}
				metrics.MarkSourceAvailable(stream.source)
			} else {
				metrics.MarkSourceError(stream.source, metrics.SourceFailureRead)
			}
			continue
		}
		valid, info, err := optionalStreamHasCompleteRecord(path)
		if err != nil {
			// The hourly root was present and discovery just returned this
			// path. A missing path here can be a rotation race, so retain the
			// last complete sample. A stable deletion is confirmed on the next
			// successful empty discovery pass above.
			metrics.MarkSourceError(stream.source, metrics.SourceFailureRead)
			setOptionalStreamAge(stream.name, state.lastValid, now)
			continue
		}
		if info.Size() == 0 {
			metrics.HLNodeStreamAgeSeconds.DeleteLabelValues(stream.name)
			state.lastValid = time.Time{}
			metrics.MarkSourceAvailable(stream.source)
			continue
		}
		if !valid {
			metrics.MarkSourceError(stream.source, metrics.SourceFailureSchema)
			setOptionalStreamAge(stream.name, state.lastValid, now)
			continue
		}
		state.lastValid = info.ModTime()
		age := now.Sub(state.lastValid)
		if age < 0 {
			age = 0
		}
		metrics.HLNodeStreamAgeSeconds.WithLabelValues(stream.name).Set(age.Seconds())
		metrics.MarkSourceValidObservation(stream.source, info.ModTime())
		metrics.MarkSourcePublication(stream.source)
	}
	metrics.MarkMonitorPublication("optional_streams")
}

func setOptionalStreamAge(stream string, lastValid, now time.Time) {
	if lastValid.IsZero() {
		return
	}
	age := now.Sub(lastValid)
	if age < 0 {
		age = 0
	}
	metrics.HLNodeStreamAgeSeconds.WithLabelValues(stream).Set(age.Seconds())
}

func optionalStreamHasCompleteRecord(path string) (bool, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, nil, err
	}
	valid, err := validateOptionalStreamRecord(f, info.Size())
	return valid, info, err
}

// Validate only a complete record from the captured descriptor size. Appends
// cannot extend this read, and a replacement path cannot change its source.
func validateOptionalStreamRecord(r io.ReaderAt, size int64) (bool, error) {
	start, end, complete, err := optionalStreamRecordBounds(r, size)
	if err != nil || !complete {
		return false, err
	}
	data := make([]byte, int(end-start))
	if _, err := r.ReadAt(data, start); err != nil {
		return false, err
	}
	data = bytes.TrimSpace(data)
	return len(data) > 0 && (data[0] == '{' || data[0] == '[') && json.Valid(data), nil
}

// A record is at most 64 MiB between newline separators, including its own
// whitespace. The scan allows one extra chunk for a separator or blank suffix.
// Exceeding either bound is a schema failure, never a successful suffix parse.
func optionalStreamRecordBounds(r io.ReaderAt, size int64) (int64, int64, bool, error) {
	if size <= 0 {
		return 0, 0, false, nil
	}
	var final [1]byte
	if _, err := r.ReadAt(final[:], size-1); err != nil {
		return 0, 0, false, err
	}
	if final[0] != '\n' {
		return 0, 0, false, nil
	}
	buffer := make([]byte, optionalStreamScanChunk)
	position, scanned, end := size-1, int64(0), int64(-1)
	lineEnd := position
	for position > 0 && scanned < optionalStreamScanLimit {
		length := min(int64(len(buffer)), position, optionalStreamScanLimit-scanned)
		position -= length
		if _, err := r.ReadAt(buffer[:length], position); err != nil {
			return 0, 0, false, err
		}
		scanned += length
		for index := length - 1; index >= 0; index-- {
			c := buffer[index]
			if end < 0 {
				if c == '\n' {
					lineEnd = position + index
					continue
				}
				if c == ' ' || c == '\t' || c == '\r' {
					continue
				}
				end = lineEnd
			} else if c == '\n' {
				start := position + index + 1
				return start, end, end-start <= optionalStreamRecordLimit, nil
			}
		}
	}
	if position == 0 && end > 0 && end <= optionalStreamRecordLimit {
		return 0, end, true, nil
	}
	return 0, 0, false, nil
}
