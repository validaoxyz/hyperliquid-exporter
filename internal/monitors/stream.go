package monitors

import (
	"bufio"
	"context"
	"errors"
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
	// rescanEvery bounds how often resolve runs; new data in the already
	// open file is still picked up every eofSleep.
	rescanEvery time.Duration
	eofSleep    time.Duration
	bufSize     int // bufio reader size; 0 means 64 KiB
	// consumeStartupRecords is reserved for a source/layout that was proven
	// to appear after the exporter started. Ordinary startup files remain
	// historical and are sought to EOF.
	consumeStartupRecords bool
	onLine                func(line string)
	onIdle                func()            // optional: once per EOF pause, for batch flushing
	onSwitch              func(path string) // optional: after switching to a new file
	onFailure             func(tailStreamFailure)
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

// tailStream follows the newest file of a stream, calling onLine for every
// newline-committed record. A file found by the first successful startup scan
// is sought to EOF to avoid replaying process counters. If that scan is
// successfully empty, the first file created later is read from byte zero.
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
		next            *pendingStreamSwitch
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

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if reader == nil || time.Since(lastScan) >= o.rescanEvery {
			lastScan = time.Now()
			latest, resolveErr := o.resolve()
			if !startupScanned && (resolveErr == nil || errors.Is(resolveErr, os.ErrNotExist)) {
				startupScanned = true
				if resolveErr == nil {
					startupPath = latest
				}
			}

			switch {
			case resolveErr != nil || latest == "":
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
				skipExisting := !o.consumeStartupRecords && startupPath != "" && latest == startupPath
				if !activate(latest, skipExisting) {
					if !sleepCtx(ctx, time.Second) {
						return
					}
					continue
				}
			default:
				needsSwitch := latest != current
				immediate := false
				if latest == current {
					latestInfo, err := os.Stat(latest)
					if err == nil {
						if currentInfo != nil && !os.SameFile(currentInfo, latestInfo) {
							needsSwitch = true
						} else if latestInfo.Size() < committedOffset+int64(len(pending))+discardedBytes {
							needsSwitch = true
							immediate = true
						}
					}
				}
				if needsSwitch && (next == nil || next.path != latest || next.immediate != immediate) {
					next = &pendingStreamSwitch{
						path:       latest,
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
		for {
			chunk, err := reader.ReadString('\n')
			if err != nil {
				if len(chunk) > 0 {
					madeProgress = true
					if discardedBytes > 0 {
						discardedBytes += int64(len(chunk))
					} else {
						pending = append(pending, chunk...)
						if len(pending) > pendingLineCap {
							if o.onFailure != nil {
								o.onFailure(tailStreamFailureRecord)
							}
							logger.WarningComponent(o.component, "%s: discarding oversized partial record (%d bytes)", o.name, len(pending))
							discardedBytes = int64(len(pending))
							pending = nil
						}
					}
				}
				if err != io.EOF {
					if o.onFailure != nil {
						o.onFailure(tailStreamFailureRead)
					}
					logger.WarningComponent(o.component, "%s: read error: %v", o.name, err)
				} else {
					cleanEOF = true
				}
				break
			}

			madeProgress = true
			if discardedBytes > 0 {
				// The delimiter commits the deliberately discarded corrupt
				// record and is the first point at which its cursor may move.
				committedOffset += discardedBytes + int64(len(chunk))
				discardedBytes = 0
				continue
			}

			line := chunk
			if len(pending) > 0 {
				line = string(pending) + chunk
				committedOffset += int64(len(pending))
				pending = pending[:0]
			}
			committedOffset += int64(len(chunk))
			o.onLine(line)
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
