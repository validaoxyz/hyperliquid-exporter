package monitors

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// pendingLineCap bounds torn-line reassembly. Once exceeded, the tailer
// discards through the next delimiter as one corrupt record; it never commits
// a cursor in the middle of that record.
const pendingLineCap = 64 << 20

const rotationDrainGrace = 2 * time.Second

// tailStreamOpts configures tailStream, the shared follow-the-newest-file
// loop used by the streaming monitors.
type tailStreamOpts struct {
	component string // logger component tag
	name      string // human-readable stream name for log lines
	// resolve returns the newest file of the stream. It must be cheap
	// (directory listings, not tree walks): it runs every rescanEvery
	// while the stream sits at EOF.
	resolve func() (string, error)
	// resolveState is the typed alternative used by sources that distinguish
	// a missing root from a readable-but-empty tree. When set, it takes
	// precedence over resolve; arbitrary traversal errors remain errors.
	resolveState func() tailStreamResolution
	// rescanEvery bounds how often resolve runs; new data in the already
	// open file is still picked up every eofSleep.
	rescanEvery time.Duration
	eofSleep    time.Duration
	bufSize     int // bufio reader size; 0 means 64 KiB
	recordCap   int // maximum committed record size; 0 means pendingLineCap
	// consumeStartupRecords is reserved for a source/layout that was proven
	// to appear after the exporter started. Ordinary startup files remain
	// historical and are sought to EOF.
	consumeStartupRecords bool
	onLine                func(line string)
	onIdle                func()            // optional: once per EOF pause, for batch flushing
	onSwitch              func(path string) // optional: after switching to a new file
	onUnavailable         func(tailStreamUnavailable)
	onFailure             func(tailStreamFailure)
}

type tailStreamUnavailable string

const (
	tailStreamUnavailableAbsent tailStreamUnavailable = "absent"
	tailStreamUnavailableEmpty  tailStreamUnavailable = "empty"
)

type tailStreamResolution struct {
	path        string
	unavailable tailStreamUnavailable
	err         error
}

type tailStreamFailure string

const (
	tailStreamFailureOpen   tailStreamFailure = "open"
	tailStreamFailureStat   tailStreamFailure = "stat"
	tailStreamFailureSeek   tailStreamFailure = "seek"
	tailStreamFailureRead   tailStreamFailure = "read"
	tailStreamFailureRecord tailStreamFailure = "record"
)

type pendingStreamSwitch struct {
	path          string
	detectedAt    time.Time
	confirmations int
	immediate     bool // same-inode truncation; no old tail can be recovered
}

type pendingStreamUnavailable struct {
	kind          tailStreamUnavailable
	detectedAt    time.Time
	confirmations int
}

// tailStream follows the newest file of a stream, calling onLine for every
// newline-committed record. The first resolver attempt establishes the startup
// boundary: a file returned by that attempt is sought to EOF to avoid replaying
// process counters, while a file discovered only later is read from byte zero.
// This remains true when the first attempt fails transiently, because a later
// file may have been created after the exporter started.
//
// Rotation is overlap-safe: the old inode remains open and must reach two
// stable EOF observations before the new file is activated. A torn old-file
// suffix gets a bounded grace period to receive its delimiter. The committed
// cursor advances only through newline-delimited records, so a partial record
// is either completed once or deliberately discarded as one corrupt record.
func tailStream(ctx context.Context, o tailStreamOpts) {
	var (
		file            *os.File
		reader          *bufio.Reader
		current         string
		currentInfo     os.FileInfo
		committedOffset int64
		pending         []byte
		discardedBytes  int64
		lastScan        time.Time
		startupScanned  bool
		startupPath     string
		startupSkip     bool
		next            *pendingStreamSwitch
		unavailable     *pendingStreamUnavailable
		lastUnavailable tailStreamUnavailable
	)

	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()

	bufSize := o.bufSize
	if bufSize == 0 {
		bufSize = 64 * 1024
	}
	if o.rescanEvery <= 0 {
		o.rescanEvery = 2 * time.Second
	}
	if o.eofSleep <= 0 {
		o.eofSleep = 100 * time.Millisecond
	}
	recordCap := o.recordCap
	if recordCap <= 0 {
		recordCap = pendingLineCap
	}

	resolve := func() tailStreamResolution {
		if o.resolveState != nil {
			return o.resolveState()
		}
		if o.resolve == nil {
			return tailStreamResolution{}
		}
		path, err := o.resolve()
		return tailStreamResolution{path: path, err: err}
	}

	activate := func(path string, skipExisting bool) bool {
		candidate, err := os.Open(path)
		if err != nil {
			if o.onFailure != nil {
				o.onFailure(tailStreamFailureOpen)
			}
			logger.WarningComponent(o.component, "%s: cannot open %s: %v", o.name, path, err)
			return false
		}
		info, err := candidate.Stat()
		if err != nil {
			_ = candidate.Close()
			if o.onFailure != nil {
				o.onFailure(tailStreamFailureStat)
			}
			logger.WarningComponent(o.component, "%s: cannot stat %s: %v", o.name, path, err)
			return false
		}
		offset := int64(0)
		if skipExisting {
			offset, err = candidate.Seek(0, io.SeekEnd)
			if err != nil {
				_ = candidate.Close()
				if o.onFailure != nil {
					o.onFailure(tailStreamFailureSeek)
				}
				logger.WarningComponent(o.component, "%s: seek to end of %s: %v", o.name, path, err)
				return false
			}
		}
		if file != nil {
			_ = file.Close()
		}
		file = candidate
		reader = bufio.NewReaderSize(candidate, bufSize)
		current = path
		currentInfo = info
		committedOffset = offset
		pending = nil
		discardedBytes = 0
		next = nil
		unavailable = nil
		lastUnavailable = ""
		if path == startupPath {
			startupSkip = false
		}
		if skipExisting {
			logger.InfoComponent(o.component, "%s: streaming from end of %s", o.name, path)
		} else {
			logger.InfoComponent(o.component, "%s: switching to %s", o.name, path)
		}
		if o.onSwitch != nil {
			o.onSwitch(path)
		}
		return true
	}

	settleUnavailable := func(kind tailStreamUnavailable) {
		if file != nil {
			_ = file.Close()
		}
		file = nil
		reader = nil
		current = ""
		currentInfo = nil
		committedOffset = 0
		pending = nil
		discardedBytes = 0
		next = nil
		unavailable = nil
		startupPath = ""
		startupSkip = false
		if lastUnavailable != kind && o.onUnavailable != nil {
			o.onUnavailable(kind)
		}
		lastUnavailable = kind
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if reader == nil || time.Since(lastScan) >= o.rescanEvery {
			lastScan = time.Now()
			resolution := resolve()
			if !startupScanned {
				startupScanned = true
				if resolution.err == nil && resolution.path != "" {
					startupPath = resolution.path
					startupSkip = !o.consumeStartupRecords
				}
			}

			switch {
			case resolution.err != nil:
				// A traversal failure is not evidence that the source is empty.
				// Cancel an uncommitted withdrawal and retain the active descriptor.
				unavailable = nil
				lastUnavailable = ""
				if reader == nil {
					if !sleepCtx(ctx, o.rescanEvery) {
						return
					}
					continue
				}
			case resolution.unavailable != "":
				// A confirmed source-level unavailability cancels startup-history
				// treatment. Recreating the same path later is recovery data and
				// must be consumed from byte zero.
				startupPath = ""
				startupSkip = false
				next = nil
				if reader == nil {
					settleUnavailable(resolution.unavailable)
					if !sleepCtx(ctx, o.rescanEvery) {
						return
					}
					continue
				}
				if unavailable == nil || unavailable.kind != resolution.unavailable {
					unavailable = &pendingStreamUnavailable{kind: resolution.unavailable, detectedAt: time.Now()}
				}
			case resolution.path == "":
				unavailable = nil
				lastUnavailable = ""
				if reader == nil {
					if !sleepCtx(ctx, o.rescanEvery) {
						return
					}
					continue
				}
			case reader == nil:
				// Only the exact file found by the first successful startup
				// scan is historical. A first file discovered after a clean
				// empty scan starts at byte zero.
				lastUnavailable = ""
				skipExisting := startupSkip && startupPath != "" && resolution.path == startupPath
				if !activate(resolution.path, skipExisting) {
					if !sleepCtx(ctx, time.Second) {
						return
					}
					continue
				}
			default:
				unavailable = nil
				lastUnavailable = ""
				needsSwitch := resolution.path != current
				immediate := false
				if resolution.path == current {
					latestInfo, err := os.Stat(resolution.path)
					if err == nil {
						if currentInfo != nil && !os.SameFile(currentInfo, latestInfo) {
							needsSwitch = true
						} else if latestInfo.Size() < committedOffset+int64(len(pending))+discardedBytes {
							needsSwitch = true
							immediate = true
						}
					}
				}
				if needsSwitch && (next == nil || next.path != resolution.path || next.immediate != immediate) {
					next = &pendingStreamSwitch{
						path:       resolution.path,
						detectedAt: time.Now(),
						immediate:  immediate,
					}
				} else if !needsSwitch && next != nil && next.path != current {
					// The candidate disappeared before it was activated.
					next = nil
				}
			}
		}

		if reader == nil {
			if !sleepCtx(ctx, o.rescanEvery) {
				return
			}
			continue
		}

		madeProgress := false
		cleanEOF := false
	readLoop:
		for {
			// ReadSlice never grows the reader's buffer. Long records arrive as
			// bounded ErrBufferFull fragments, so recordCap is a real memory bound
			// rather than a check performed after an unbounded ReadString allocation.
			fragment, err := reader.ReadSlice('\n')
			if len(fragment) > 0 {
				madeProgress = true
				if discardedBytes > 0 {
					discardedBytes += int64(len(fragment))
				} else if len(pending) > recordCap-len(fragment) {
					recordBytes := len(pending) + len(fragment)
					if o.onFailure != nil {
						o.onFailure(tailStreamFailureRecord)
					}
					logger.WarningComponent(o.component, "%s: discarding oversized record (%d+ bytes)", o.name, recordBytes)
					discardedBytes = int64(recordBytes)
					pending = nil
				} else {
					pending = append(pending, fragment...)
				}
			}

			switch err {
			case nil:
				if discardedBytes > 0 {
					// The delimiter commits the deliberately discarded corrupt
					// record and is the first point at which its cursor may move.
					committedOffset += discardedBytes
					discardedBytes = 0
					continue
				}
				committedOffset += int64(len(pending))
				line := string(pending)
				pending = pending[:0]
				o.onLine(line)
			case bufio.ErrBufferFull:
				continue
			case io.EOF:
				cleanEOF = true
				break readLoop
			default:
				if o.onFailure != nil {
					o.onFailure(tailStreamFailureRead)
				}
				logger.WarningComponent(o.component, "%s: read error: %v", o.name, err)
				break readLoop
			}
		}

		if cleanEOF && o.onIdle != nil {
			o.onIdle()
		}

		if next != nil {
			if next.immediate {
				// A same-inode truncation invalidates the unread old suffix.
				// Open the replacement before closing the current descriptor.
				if activate(next.path, false) {
					continue
				}
			} else if madeProgress {
				next.confirmations = 0
			} else if len(pending) == 0 && discardedBytes == 0 {
				next.confirmations++
				if next.confirmations >= 2 && activate(next.path, false) {
					continue
				}
			} else if time.Since(next.detectedAt) >= rotationDrainGrace {
				if o.onFailure != nil {
					o.onFailure(tailStreamFailureRecord)
				}
				logger.WarningComponent(o.component, "%s: dropping unterminated tail while switching from %s to %s", o.name, current, next.path)
				if activate(next.path, false) {
					continue
				}
			}
		}

		if unavailable != nil {
			kind := unavailable.kind
			switch {
			case madeProgress:
				unavailable.confirmations = 0
				unavailable.detectedAt = time.Now()
			case !cleanEOF:
				// A read failure cannot confirm that the old descriptor drained.
			case len(pending) == 0 && discardedBytes == 0:
				unavailable.confirmations++
				if unavailable.confirmations >= 2 {
					settleUnavailable(kind)
					continue
				}
			case time.Since(unavailable.detectedAt) >= rotationDrainGrace:
				if o.onFailure != nil {
					o.onFailure(tailStreamFailureRecord)
				}
				logger.WarningComponent(o.component, "%s: dropping unterminated tail while source became %s", o.name, kind)
				settleUnavailable(kind)
				continue
			}
		}

		if !sleepCtx(ctx, o.eofSleep) {
			return
		}
	}
}

// sleepCtx sleeps for d or until ctx is done; reports false when ctx won.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
