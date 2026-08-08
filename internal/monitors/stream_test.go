package monitors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const streamTestTimeout = 2 * time.Second

func appendTestFile(t *testing.T, path, value string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(value); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitStreamValue(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for stream value")
		return ""
	}
}

func waitUnavailable(t *testing.T, ch <-chan tailStreamUnavailable) tailStreamUnavailable {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for unavailable transition")
		return ""
	}
}

func runTestTail(t *testing.T, initialPath string) (setPath func(string), lines <-chan string, switched <-chan string, stop func()) {
	t.Helper()
	var current atomic.Value
	current.Store(initialPath)
	lineCh := make(chan string, 16)
	switchCh := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component:   "stream_test",
			name:        "test stream",
			resolve:     func() (string, error) { return current.Load().(string), nil },
			rescanEvery: 10 * time.Millisecond,
			eofSleep:    5 * time.Millisecond,
			onLine:      func(line string) { lineCh <- line },
			onSwitch:    func(path string) { switchCh <- path },
		})
	}()
	return func(path string) { current.Store(path) }, lineCh, switchCh, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("tailStream did not stop after cancellation")
		}
	}
}

func TestTailStreamSkipsOnlyStartupHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "startup")
	if err := os.WriteFile(path, []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, lines, switched, stop := runTestTail(t, path)
	defer stop()
	if got := waitStreamValue(t, switched); got != path {
		t.Fatalf("switched to %q, want %q", got, path)
	}
	appendTestFile(t, path, "fresh\n")
	if got := waitStreamValue(t, lines); got != "fresh\n" {
		t.Fatalf("line = %q, want fresh record only", got)
	}
}

func TestTailStreamReadsFirstFileCreatedAfterEmptyStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "late")
	setPath, lines, switched, stop := runTestTail(t, "")
	defer stop()

	// Allow at least one successful empty startup scan.
	time.Sleep(25 * time.Millisecond)
	if err := os.WriteFile(path, []byte("late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setPath(path)
	if got := waitStreamValue(t, switched); got != path {
		t.Fatalf("switched to %q, want %q", got, path)
	}
	if got := waitStreamValue(t, lines); got != "late\n" {
		t.Fatalf("line = %q, want post-start first record", got)
	}
}

func TestTailStreamReadsFirstFileAfterENOENTStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "late")
	var current atomic.Value
	current.Store("")
	lines := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component: "stream_test",
			name:      "ENOENT startup",
			resolve: func() (string, error) {
				resolved := current.Load().(string)
				if resolved == "" {
					return "", os.ErrNotExist
				}
				return resolved, nil
			},
			rescanEvery: 10 * time.Millisecond,
			eofSleep:    5 * time.Millisecond,
			onLine:      func(line string) { lines <- line },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("tailStream did not stop after cancellation")
		}
	})

	time.Sleep(25 * time.Millisecond)
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current.Store(path)
	if got := waitStreamValue(t, lines); got != "first\n" {
		t.Fatalf("late first line = %q, want first record", got)
	}
}

func TestTailStreamReadsFirstFileAfterTransientStartupFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "late")
	var current atomic.Value
	current.Store("")
	firstAttempt := make(chan struct{})
	var attempts atomic.Int32
	lines := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component: "stream_test",
			name:      "transient startup failure",
			resolve: func() (string, error) {
				if attempts.Add(1) == 1 {
					close(firstAttempt)
					return "", errors.New("transient discovery failure")
				}
				return current.Load().(string), nil
			},
			rescanEvery: 10 * time.Millisecond,
			eofSleep:    5 * time.Millisecond,
			onLine:      func(line string) { lines <- line },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("tailStream did not stop after cancellation")
		}
	})

	select {
	case <-firstAttempt:
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for initial resolver failure")
	}
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current.Store(path)
	if got := waitStreamValue(t, lines); got != "first\n" {
		t.Fatalf("late first line = %q, want first record", got)
	}
}

func TestTailStreamDrainsBeforeUnavailableAndRecoversSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same-path")
	if err := os.WriteFile(path, []byte("startup-history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldWriter, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldWriter.Close() })

	var resolution atomic.Value
	resolution.Store(tailStreamResolution{path: path})
	unavailableSeen := make(chan struct{})
	releaseUnavailable := make(chan struct{})
	var blocked atomic.Bool
	lines := make(chan string, 8)
	switches := make(chan string, 8)
	withdrawals := make(chan tailStreamUnavailable, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component: "stream_test",
			name:      "typed unavailable",
			resolveState: func() tailStreamResolution {
				current := resolution.Load().(tailStreamResolution)
				if current.unavailable != "" && blocked.CompareAndSwap(false, true) {
					close(unavailableSeen)
					<-releaseUnavailable
				}
				return current
			},
			rescanEvery: 10 * time.Millisecond,
			eofSleep:    10 * time.Millisecond,
			onLine:      func(line string) { lines <- line },
			onSwitch:    func(path string) { switches <- path },
			onUnavailable: func(kind tailStreamUnavailable) {
				withdrawals <- kind
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("typed tail did not stop")
		}
	})

	if got := waitStreamValue(t, switches); got != path {
		t.Fatalf("startup switch = %q, want %q", got, path)
	}
	resolution.Store(tailStreamResolution{unavailable: tailStreamUnavailableAbsent})
	select {
	case <-unavailableSeen:
	case <-time.After(streamTestTimeout):
		t.Fatal("typed unavailability was not resolved")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := oldWriter.WriteString("old-fd-tail\n"); err != nil {
		t.Fatal(err)
	}
	close(releaseUnavailable)

	if got := waitStreamValue(t, lines); got != "old-fd-tail\n" {
		t.Fatalf("drained line = %q, want old descriptor tail", got)
	}
	select {
	case got := <-withdrawals:
		if got != tailStreamUnavailableAbsent {
			t.Fatalf("withdrawal = %q, want absent", got)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("old descriptor did not settle unavailable")
	}

	if _, err := oldWriter.WriteString("after-callback\n"); err != nil {
		t.Fatal(err)
	}
	if err := oldWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("recovered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolution.Store(tailStreamResolution{path: path})
	if got := waitStreamValue(t, switches); got != path {
		t.Fatalf("recovery switch = %q, want %q", got, path)
	}
	if got := waitStreamValue(t, lines); got != "recovered\n" {
		t.Fatalf("recovery line = %q, want recreated path from byte zero", got)
	}
	select {
	case got := <-lines:
		t.Fatalf("old descriptor emitted after withdrawal: %q", got)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestTailStreamUnavailableTransitionsAreDeduplicated(t *testing.T) {
	var resolution atomic.Value
	resolution.Store(tailStreamResolution{unavailable: tailStreamUnavailableAbsent})
	withdrawals := make(chan tailStreamUnavailable, 8)
	failures := make(chan tailStreamFailure, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component:    "stream_test",
			name:         "unavailable transitions",
			resolveState: func() tailStreamResolution { return resolution.Load().(tailStreamResolution) },
			rescanEvery:  10 * time.Millisecond,
			eofSleep:     5 * time.Millisecond,
			onLine:       func(string) {},
			onUnavailable: func(kind tailStreamUnavailable) {
				withdrawals <- kind
			},
			onFailure: func(failure tailStreamFailure) { failures <- failure },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("typed tail did not stop")
		}
	})

	if got := waitUnavailable(t, withdrawals); got != tailStreamUnavailableAbsent {
		t.Fatalf("first transition = %q, want absent", got)
	}
	select {
	case got := <-withdrawals:
		t.Fatalf("repeated state published again: %q", got)
	case <-time.After(35 * time.Millisecond):
	}
	resolution.Store(tailStreamResolution{err: errors.New("traversal failure")})
	select {
	case got := <-withdrawals:
		t.Fatalf("traversal error published unavailable: %q", got)
	case <-time.After(35 * time.Millisecond):
	}
	resolution.Store(tailStreamResolution{unavailable: tailStreamUnavailableAbsent})
	if got := waitUnavailable(t, withdrawals); got != tailStreamUnavailableAbsent {
		t.Fatalf("same absent state after error = %q, want recovery publication", got)
	}

	resolution.Store(tailStreamResolution{unavailable: tailStreamUnavailableEmpty})
	if got := waitUnavailable(t, withdrawals); got != tailStreamUnavailableEmpty {
		t.Fatalf("second transition = %q, want empty", got)
	}
	resolution.Store(tailStreamResolution{err: errors.New("empty traversal failure")})
	select {
	case got := <-withdrawals:
		t.Fatalf("empty traversal error published unavailable: %q", got)
	case <-time.After(35 * time.Millisecond):
	}
	resolution.Store(tailStreamResolution{unavailable: tailStreamUnavailableEmpty})
	if got := waitUnavailable(t, withdrawals); got != tailStreamUnavailableEmpty {
		t.Fatalf("same empty state after error = %q, want recovery publication", got)
	}

	resolution.Store(tailStreamResolution{path: filepath.Join(t.TempDir(), "missing")})
	select {
	case got := <-failures:
		if got != tailStreamFailureOpen {
			t.Fatalf("found-path failure = %q, want open", got)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("found-path open failure was not reported")
	}
	resolution.Store(tailStreamResolution{unavailable: tailStreamUnavailableEmpty})
	if got := waitUnavailable(t, withdrawals); got != tailStreamUnavailableEmpty {
		t.Fatalf("same empty state after open failure = %q, want recovery publication", got)
	}
}

func TestTailStreamTraversalErrorRetainsActiveDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active")
	if err := os.WriteFile(path, []byte("startup-history\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var resolution atomic.Value
	resolution.Store(tailStreamResolution{path: path})
	lines := make(chan string, 4)
	switches := make(chan string, 2)
	withdrawals := make(chan tailStreamUnavailable, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component:    "stream_test",
			name:         "active traversal error",
			resolveState: func() tailStreamResolution { return resolution.Load().(tailStreamResolution) },
			rescanEvery:  10 * time.Millisecond,
			eofSleep:     5 * time.Millisecond,
			onLine:       func(line string) { lines <- line },
			onSwitch:     func(path string) { switches <- path },
			onUnavailable: func(kind tailStreamUnavailable) {
				withdrawals <- kind
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("typed tail did not stop")
		}
	})

	if got := waitStreamValue(t, switches); got != path {
		t.Fatalf("startup switch = %q, want %q", got, path)
	}
	resolution.Store(tailStreamResolution{err: errors.New("transient traversal failure")})
	writer, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("still-readable\n"); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := waitStreamValue(t, lines); got != "still-readable\n" {
		t.Fatalf("active descriptor line = %q, want retained read", got)
	}
	select {
	case got := <-withdrawals:
		t.Fatalf("traversal error withdrew active source: %q", got)
	case <-time.After(35 * time.Millisecond):
	}
	resolution.Store(tailStreamResolution{path: path})
}

func TestTailStreamDrainsOldFileBeforeRotation(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	if err := os.WriteFile(oldPath, []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	setPath, lines, switched, stop := runTestTail(t, oldPath)
	defer stop()
	_ = waitStreamValue(t, switched)

	appendTestFile(t, oldPath, "old-tail\n")
	if err := os.WriteFile(newPath, []byte("new-prefix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setPath(newPath)

	if got := waitStreamValue(t, lines); got != "old-tail\n" {
		t.Fatalf("first line = %q, want drained old tail", got)
	}
	if got := waitStreamValue(t, switched); got != newPath {
		t.Fatalf("rotation target = %q, want %q", got, newPath)
	}
	if got := waitStreamValue(t, lines); got != "new-prefix\n" {
		t.Fatalf("second line = %q, want new prefix", got)
	}
}

func TestTailStreamRetainsTornRecordUntilDelimiter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "torn")
	setPath, lines, switched, stop := runTestTail(t, "")
	defer stop()
	time.Sleep(25 * time.Millisecond)
	if err := os.WriteFile(path, []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	setPath(path)
	_ = waitStreamValue(t, switched)

	select {
	case got := <-lines:
		t.Fatalf("unterminated input emitted early as %q", got)
	case <-time.After(25 * time.Millisecond):
	}
	appendTestFile(t, path, "ial\n")
	if got := waitStreamValue(t, lines); got != "partial\n" {
		t.Fatalf("reassembled line = %q", got)
	}
}

func TestTailStreamEnforcesRecordCapBeforeDelimiterAndRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bounded")
	var current atomic.Value
	current.Store("")
	lines := make(chan string, 4)
	failures := make(chan tailStreamFailure, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component:   "stream_test",
			name:        "bounded stream",
			resolve:     func() (string, error) { return current.Load().(string), nil },
			rescanEvery: 10 * time.Millisecond,
			eofSleep:    5 * time.Millisecond,
			bufSize:     8,
			recordCap:   32,
			onLine:      func(line string) { lines <- line },
			onFailure:   func(failure tailStreamFailure) { failures <- failure },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("tailStream did not stop after cancellation")
		}
	})

	time.Sleep(25 * time.Millisecond)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	current.Store(path)
	select {
	case got := <-failures:
		if got != tailStreamFailureRecord {
			t.Fatalf("oversized torn record failure = %q, want record", got)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for oversized record failure")
	}
	select {
	case got := <-lines:
		t.Fatalf("oversized torn record emitted as %q", got)
	case <-time.After(25 * time.Millisecond):
	}

	appendTestFile(t, path, "\nok\n")
	if got := waitStreamValue(t, lines); got != "ok\n" {
		t.Fatalf("line after oversized torn record = %q, want ok", got)
	}

	appendTestFile(t, path, strings.Repeat("y", 64)+"\nafter\n")
	select {
	case got := <-failures:
		if got != tailStreamFailureRecord {
			t.Fatalf("oversized terminated record failure = %q, want record", got)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for terminated oversized record failure")
	}
	if got := waitStreamValue(t, lines); got != "after\n" {
		t.Fatalf("line after terminated oversized record = %q, want after", got)
	}

	acceptedBoundary := strings.Repeat("z", 31) + "\n"
	appendTestFile(t, path, acceptedBoundary)
	if got := waitStreamValue(t, lines); got != acceptedBoundary {
		t.Fatalf("record exactly at cap = %q, want accepted", got)
	}
	appendTestFile(t, path, strings.Repeat("z", 32)+"\nboundary-recovery\n")
	select {
	case got := <-failures:
		if got != tailStreamFailureRecord {
			t.Fatalf("record one byte over cap failure = %q, want record", got)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for cap+1 record failure")
	}
	if got := waitStreamValue(t, lines); got != "boundary-recovery\n" {
		t.Fatalf("line after cap+1 record = %q, want recovery", got)
	}
}

func TestTailStreamDetectsSameInodeTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same")
	if err := os.WriteFile(path, []byte("long-startup-history\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, lines, switched, stop := runTestTail(t, path)
	defer stop()
	_ = waitStreamValue(t, switched)
	appendTestFile(t, path, "before\n")
	if got := waitStreamValue(t, lines); got != "before\n" {
		t.Fatalf("line before truncation = %q", got)
	}

	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := waitStreamValue(t, switched); got != path {
		t.Fatalf("truncation switch = %q, want %q", got, path)
	}
	if got := waitStreamValue(t, lines); got != "after\n" {
		t.Fatalf("line after truncation = %q", got)
	}
}

func TestTailStreamReportsBoundedFailureStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	missing := filepath.Join(t.TempDir(), "missing")
	failures := make(chan tailStreamFailure, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component:   "stream_test",
			name:        "missing stream",
			resolve:     func() (string, error) { return missing, nil },
			rescanEvery: 5 * time.Millisecond,
			eofSleep:    5 * time.Millisecond,
			onLine:      func(string) {},
			onFailure:   func(failure tailStreamFailure) { failures <- failure },
		})
	}()
	select {
	case got := <-failures:
		if got != tailStreamFailureOpen {
			t.Fatalf("failure = %q, want open", got)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for failure callback")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(streamTestTimeout):
		t.Fatal("tail did not cancel after open failure")
	}
}
