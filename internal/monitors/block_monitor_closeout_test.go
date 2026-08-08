package monitors

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetBlockBaselinesForTest(t *testing.T) {
	t.Helper()
	lastBlockTimeMu.Lock()
	oldTimes := lastBlockTimes
	oldHeights := lastBlockHeights
	lastBlockTimes = make(map[string]time.Time)
	lastBlockHeights = make(map[string]int64)
	lastBlockTimeMu.Unlock()
	t.Cleanup(func() {
		lastBlockTimeMu.Lock()
		lastBlockTimes = oldTimes
		lastBlockHeights = oldHeights
		lastBlockTimeMu.Unlock()
	})
}

func blockTimeRecord(height int64, second int, applyDuration string) string {
	return fmt.Sprintf(
		`{"height":%d,"block_time":"2026-08-08T00:00:%02d.000000000","begin_block_wall_time":"","apply_duration":%s}`,
		height,
		second,
		applyDuration,
	)
}

func legacyBlockTimeRecord(height int64, second int, applyDuration string) string {
	return fmt.Sprintf(
		`{"height":%d,"block_time":"2026-08-08T00:00:%02d.000000000","apply_duration":%s}`,
		height,
		second,
		applyDuration,
	)
}

func TestDetectBlockLayoutTracksLateAndPreferredLayouts(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	assertLayout := func(want blockLayout) {
		t.Helper()
		got, err := detectBlockLayout(home)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("layout = %v, want %v", got, want)
		}
	}

	assertLayout(blockLayoutNone)
	if err := os.Mkdir(filepath.Join(data, "block_times"), 0o700); err != nil {
		t.Fatal(err)
	}
	assertLayout(blockLayoutLegacy)
	if err := os.Mkdir(filepath.Join(data, "node_fast_block_times"), 0o700); err != nil {
		t.Fatal(err)
	}
	assertLayout(blockLayoutDual)
	if err := os.RemoveAll(filepath.Join(data, "node_fast_block_times")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(data, "node_slow_block_times"), 0o700); err != nil {
		t.Fatal(err)
	}
	assertLayout(blockLayoutDual)
}

func TestTailStreamCanConsumeProvenLateStartupFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late-layout")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailStream(ctx, tailStreamOpts{
			component:             "block_test",
			name:                  "late block layout",
			resolve:               func() (string, error) { return path, nil },
			rescanEvery:           5 * time.Millisecond,
			eofSleep:              5 * time.Millisecond,
			consumeStartupRecords: true,
			onLine:                func(line string) { lines <- line },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(streamTestTimeout):
			t.Fatal("tailStream did not stop")
		}
	})
	if got := waitStreamValue(t, lines); got != "first\n" {
		t.Fatalf("startup line = %q, want first record", got)
	}
}

func TestBlockTimeParserRejectsOverflowAndPreservesMonotonicBaseline(t *testing.T) {
	resetBlockBaselinesForTest(t)
	const state = "closeout-fast"
	if err := parseBlockTimeLine(blockTimeRecord(10, 10, "0.1"), state); err != nil {
		t.Fatalf("first valid record: %v", err)
	}
	if err := parseBlockTimeLine(blockTimeRecord(9, 9, "0.1"), state); err == nil {
		t.Fatal("regressing block record was accepted")
	}
	lastBlockTimeMu.RLock()
	retainedHeight := lastBlockHeights[state]
	retainedTime := lastBlockTimes[state]
	lastBlockTimeMu.RUnlock()
	if retainedHeight != 10 || retainedTime.Second() != 10 {
		t.Fatalf("regression changed baseline to height=%d time=%s", retainedHeight, retainedTime)
	}
	if err := parseBlockTimeLine(blockTimeRecord(11, 11, "0.1"), state); err != nil {
		t.Fatalf("successor after rejected regression: %v", err)
	}

	const overflowState = "closeout-overflow"
	if err := parseBlockTimeLine(blockTimeRecord(1, 1, "1e308"), overflowState); err == nil {
		t.Fatal("apply-duration millisecond overflow was accepted")
	}
	lastBlockTimeMu.RLock()
	_, hasTime := lastBlockTimes[overflowState]
	_, hasHeight := lastBlockHeights[overflowState]
	lastBlockTimeMu.RUnlock()
	if hasTime || hasHeight {
		t.Fatal("overflowing record advanced the block baseline")
	}
	largeDuration := float64(1e308)
	if !math.IsInf(largeDuration*1000, 1) {
		t.Fatal("test fixture no longer overflows float64 milliseconds")
	}
}

func TestLegacyBlockTimeParserRejectsOverflowAndRegression(t *testing.T) {
	resetBlockBaselinesForTest(t)
	if err := parseLegacyBlockTimeLine(legacyBlockTimeRecord(10, 10, "0.1")); err != nil {
		t.Fatalf("first valid legacy record: %v", err)
	}
	if err := parseLegacyBlockTimeLine(legacyBlockTimeRecord(9, 9, "0.1")); err == nil {
		t.Fatal("regressing legacy block record was accepted")
	}
	if err := parseLegacyBlockTimeLine(legacyBlockTimeRecord(11, 11, "0.1")); err != nil {
		t.Fatalf("legacy successor after rejected regression: %v", err)
	}
	if err := parseLegacyBlockTimeLine(legacyBlockTimeRecord(12, 12, "1e308")); err == nil {
		t.Fatal("legacy apply-duration millisecond overflow was accepted")
	}
}
