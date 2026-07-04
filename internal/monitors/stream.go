package monitors

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// pendingLineCap bounds the torn-line reassembly buffer. Consensus lines
// reach single-digit MBs; anything past this is a corrupt stream and gets
// dropped rather than ballooning memory.
const pendingLineCap = 64 << 20

// tailStreamOpts configures tailStream, the shared follow-the-newest-file
// loop used by the streaming monitors.
type tailStreamOpts struct {
	component string // logger component tag
	name      string // human-readable stream name for log lines
	// resolve returns the newest file of the stream. It must be cheap
	// (directory listings, not tree walks): it runs every rescanEvery
	// while the stream sits at EOF.
	resolve func() (string, error)
	// rescanEvery bounds how often resolve runs; new data in the already
	// open file is still picked up every eofSleep.
	rescanEvery time.Duration
	eofSleep    time.Duration
	bufSize     int // bufio reader size; 0 means 64 KiB
	onLine      func(line string)
	onIdle      func()            // optional: once per EOF pause, for batch flushing
	onSwitch    func(path string) // optional: after switching to a new file
}

// tailStream follows the newest file of a stream, calling onLine for every
// complete line. On startup it seeks to the end of the current file (the
// no-replay rule: replaying history would double-count counters). A partial
// line at EOF is buffered and reassembled once the writer completes it
// instead of being consumed as a torn line. Resolve and open failures are
// logged and retried, not fatal: streams legitimately appear late
// (validator-only dirs) and roll over hourly.
//
// This replaces a per-monitor pattern that re-ran a full recursive
// directory walk on every 10ms EOF pause, which is where most of the
// exporter's CPU used to go.
func tailStream(ctx context.Context, o tailStreamOpts) {
	var (
		file     *os.File
		reader   *bufio.Reader
		current  string
		pending  []byte
		lastScan time.Time
	)
	firstRun := true

	defer func() {
		if file != nil {
			file.Close()
		}
	}()

	bufSize := o.bufSize
	if bufSize == 0 {
		bufSize = 64 * 1024
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if reader == nil || time.Since(lastScan) >= o.rescanEvery {
			lastScan = time.Now()
			latest, err := o.resolve()
			switch {
			case err != nil || latest == "":
				if reader == nil {
					// stream not there yet (non-validator node, fresh
					// install): check again in a full rescan period
					if !sleepCtx(ctx, o.rescanEvery) {
						return
					}
					continue
				}
				// keep draining the file we already have
			case latest != current || reader == nil:
				// reader == nil covers a failed open of a since-vanished
				// newer file: without it, latest == current would skip the
				// reopen forever
				reopenSame := latest == current
				if file != nil {
					file.Close()
					file, reader = nil, nil
				}
				pending = nil
				f, err := os.Open(latest)
				if err != nil {
					logger.WarningComponent(o.component, "%s: cannot open %s: %v", o.name, latest, err)
					if !sleepCtx(ctx, time.Second) {
						return
					}
					continue
				}
				if reopenSame && !firstRun {
					// re-opening the file we already consumed: resume at the
					// end rather than re-emitting it from byte 0
					if _, err := f.Seek(0, io.SeekEnd); err != nil {
						f.Close()
						if !sleepCtx(ctx, time.Second) {
							return
						}
						continue
					}
				}
				if firstRun {
					if _, err := f.Seek(0, io.SeekEnd); err != nil {
						logger.WarningComponent(o.component, "%s: seek to end of %s: %v", o.name, latest, err)
						f.Close()
						if !sleepCtx(ctx, time.Second) {
							return
						}
						continue
					}
					logger.InfoComponent(o.component, "%s: streaming from end of %s", o.name, latest)
				} else {
					logger.InfoComponent(o.component, "%s: switching to %s", o.name, latest)
				}
				firstRun = false
				file = f
				reader = bufio.NewReaderSize(file, bufSize)
				current = latest
				if o.onSwitch != nil {
					o.onSwitch(latest)
				}
			}
		}

		if reader == nil {
			if !sleepCtx(ctx, o.rescanEvery) {
				return
			}
			continue
		}

		for {
			chunk, err := reader.ReadString('\n')
			if err != nil {
				if len(chunk) > 0 {
					// torn tail: keep it until the writer finishes the line
					pending = append(pending, chunk...)
					if len(pending) > pendingLineCap {
						logger.WarningComponent(o.component, "%s: dropping oversized partial line (%d bytes)", o.name, len(pending))
						pending = nil
					}
				}
				if err != io.EOF {
					logger.WarningComponent(o.component, "%s: read error: %v", o.name, err)
				}
				break
			}
			line := chunk
			if len(pending) > 0 {
				line = string(pending) + chunk
				pending = pending[:0]
			}
			o.onLine(line)
		}

		if o.onIdle != nil {
			o.onIdle()
		}
		if !sleepCtx(ctx, o.eofSleep) {
			return
		}
	}
}

// sleepCtx sleeps for d or until ctx is done; reports false when ctx won.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
