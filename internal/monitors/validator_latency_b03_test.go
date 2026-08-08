package monitors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func rawLatencyFixture(at time.Time, round int64, latency float64) string {
	return fmt.Sprintf(`{"time":%q,"round":%d,"latency":%g}`, at.UTC().Format("2006-01-02T15:04:05.999999999"), round, latency)
}

func emaFixture(at time.Time, rows [][2]any) string {
	data, _ := json.Marshal([]any{at.UTC().Format("2006-01-02T15:04:05.999999999"), rows})
	return string(data)
}

func TestRawValidatorLatencyRetainsFragmentIdentityAndPollPeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1")
	now := time.Now().UTC().Truncate(time.Second)
	line := rawLatencyFixture(now, 10, 0.2)
	cut := len(line) / 2
	if err := os.WriteFile(path, []byte(line[:cut]), 0o644); err != nil {
		t.Fatal(err)
	}
	monitor := NewValidatorLatencyMonitor(&config.Config{})
	state := &rawLatencyState{}
	result, err := monitor.readValidatorLatency(state, path)
	if err != nil || result.valid != 0 || state.hasData {
		t.Fatalf("partial raw record committed: %+v, %v", result, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line[cut:] + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	result, err = monitor.readValidatorLatency(state, path)
	if err != nil || result.valid != 1 || state.latency != 0.2 {
		t.Fatalf("completed raw record = %+v, state=%+v, err=%v", result, state, err)
	}
	result, err = monitor.readValidatorLatency(state, path)
	if err != nil || result.valid != 0 {
		t.Fatalf("raw record replayed: %+v, %v", result, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(
		rawLatencyFixture(now.Add(time.Second), 11, 0.3)+"\n"+
			rawLatencyFixture(now.Add(2*time.Second), 12, 1.7)+"\n"+
			rawLatencyFixture(now.Add(3*time.Second), 13, 0.4)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = monitor.readValidatorLatency(state, path)
	if err != nil || result.valid != 3 || !result.hasPeak || result.peak != 1.7 || state.latency != 0.4 {
		t.Fatalf("replacement/peak result = %+v, state=%+v, err=%v", result, state, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"time":"2026-08-08T00:00:00","round":null,"latency":0}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = monitor.readValidatorLatency(state, path)
	if err != nil || result.valid != 0 || !result.invalid {
		t.Fatalf("null required raw field accepted: %+v, %v", result, err)
	}
}

func TestValidatorEMAMixedZerosSentinelsAndNull(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rows := make([][2]any, 0, 96)
	for i := 0; i < 95; i++ {
		rows = append(rows, [2]any{fmt.Sprintf("0x%040x", i), float64(0)})
	}
	rows = append(rows, [2]any{"0xffffffffffffffffffffffffffffffffffffffff", 0.41})
	_, state, snapshot, err := parseValidatorEMASnapshot([]byte(emaFixture(now, rows)))
	if err != nil || state != "measured" || len(snapshot) != 96 || snapshot["0xffffffffffffffffffffffffffffffffffffffff"] != 0.41 {
		t.Fatalf("95-zero mixed EMA = %s, len=%d, err=%v", state, len(snapshot), err)
	}
	for _, sentinel := range []float64{0.4, 0.3999999999999996, 0.3999999999999999} {
		_, state, snapshot, err = parseValidatorEMASnapshot([]byte(emaFixture(now, [][2]any{{"sentinel", sentinel}, {"real", 0.400001}})))
		if err != nil || state != "measured" || len(snapshot) != 1 || snapshot["real"] != 0.400001 {
			t.Fatalf("sentinel %v = %s, %v, %v", sentinel, state, snapshot, err)
		}
	}
	if _, _, _, err := parseValidatorEMASnapshot([]byte(fmt.Sprintf(`[%q,[["a",null],["b",0.1]]]`, now.Format("2006-01-02T15:04:05.999999999")))); err == nil {
		t.Fatal("mixed EMA null was accepted as a legitimate zero")
	}
}

func TestValidatorEMATwoCompleteZeroEpochsRemainInitializing(t *testing.T) {
	nodeHome := t.TempDir()
	base := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	path := filepath.Join(nodeHome, "data", "validator_latency_ema", base.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	makeEpoch := func(start time.Time) string {
		rows := make([][2]any, 0, 96)
		for i := 0; i < 96; i++ {
			rows = append(rows, [2]any{fmt.Sprintf("0x%040x", i), float64(0)})
		}
		var out strings.Builder
		for frame := 0; frame < 18; frame++ {
			out.WriteString(emaFixture(start.Add(time.Duration(frame)*time.Second), rows))
			out.WriteByte('\n')
		}
		return out.String()
	}

	if err := os.WriteFile(path, []byte(makeEpoch(base)), 0o644); err != nil {
		t.Fatal(err)
	}
	monitor := NewValidatorLatencyMonitor(&config.Config{NodeHome: nodeHome})
	firstLast := base.Add(17 * time.Second)
	if err := monitor.processEMAFileAt(firstLast.Add(time.Second)); err != nil || !monitor.lastEMATime.Equal(firstLast) {
		t.Fatalf("first 18x96 zero epoch: time=%v err=%v", monitor.lastEMATime, err)
	}

	secondBase := base.Add(30 * time.Second)
	replacement := path + ".next"
	if err := os.WriteFile(replacement, []byte(makeEpoch(secondBase)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	secondLast := secondBase.Add(17 * time.Second)
	if err := monitor.processEMAFileAt(secondLast.Add(time.Second)); err != nil || !monitor.lastEMATime.Equal(secondLast) {
		t.Fatalf("second 18x96 zero epoch: time=%v err=%v", monitor.lastEMATime, err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLConsensusValidatorLatencyEMAState, map[string]string{"state": "initializing"}); !ok || value != 1 {
		t.Fatalf("two zero epochs state = %v, %v", value, ok)
	}
	monitor.withdrawEMA(true)
}

func TestRawValidatorLatencyPeakSharesSnapshotWithdrawal(t *testing.T) {
	nodeHome := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	signer := "0x1111111111111111111111111111111111111111"
	path := filepath.Join(nodeHome, "data", "validator_latency", signer, "hourly", now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := rawLatencyFixture(now, 1, 0.1) + "\n" + rawLatencyFixture(now.Add(time.Second), 2, 0.9) + "\n" + rawLatencyFixture(now.Add(2*time.Second), 3, 0.2) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	monitor := NewValidatorLatencyMonitor(&config.Config{NodeHome: nodeHome})
	if err := monitor.processLatencyFilesAt(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLConsensusValidatorLatencyPollMax, map[string]string{"signer": signer}); !ok || value != 0.9 {
		t.Fatalf("within-poll peak = %v, %v", value, ok)
	}
	if err := os.RemoveAll(filepath.Join(nodeHome, "data", "validator_latency")); err != nil {
		t.Fatal(err)
	}
	if err := monitor.processLatencyFilesAt(now.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if b03CollectorHasLabels(t, metrics.HLConsensusValidatorLatencyPollMax, map[string]string{"signer": signer}) {
		t.Fatal("raw source deletion retained scrape-window peak")
	}
}

func TestValidatorEMAIncrementalInitializationRollbackDeleteAndRecovery(t *testing.T) {
	nodeHome := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(nodeHome, "data", "validator_latency_ema", now.Format("20060102"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	measured := emaFixture(now, [][2]any{{"a", 0.1}, {"b", 0.4}})
	if err := os.WriteFile(path, []byte(measured+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	monitor := NewValidatorLatencyMonitor(&config.Config{NodeHome: nodeHome})
	if err := monitor.processEMAFileAt(now.Add(time.Second)); err != nil || !monitor.lastEMATime.Equal(now) {
		t.Fatalf("initial measured EMA: time=%v err=%v", monitor.lastEMATime, err)
	}
	initializingTime := now.Add(2 * time.Second)
	initializing := emaFixture(initializingTime, [][2]any{{"a", 0.0}, {"b", 0.0}})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.TrimSuffix(initializing, "]")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := monitor.processEMAFileAt(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !monitor.lastEMATime.Equal(now) {
		t.Fatal("incomplete all-zero EMA generation committed")
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("]\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := monitor.processEMAFileAt(now.Add(4 * time.Second)); err != nil || !monitor.lastEMATime.Equal(initializingTime) {
		t.Fatalf("complete initializing EMA: time=%v err=%v", monitor.lastEMATime, err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLConsensusValidatorLatencyEMAState, map[string]string{"state": "initializing"}); !ok || value != 1 {
		t.Fatalf("initializing state = %v, %v", value, ok)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := monitor.processEMAFileAt(now.Add(5 * time.Second)); err != nil || !monitor.lastEMATime.IsZero() {
		t.Fatalf("deleted EMA did not withdraw/reset: time=%v err=%v", monitor.lastEMATime, err)
	}
	if len(b03CollectorRows(t, metrics.HLConsensusValidatorLatencyEMAState)) != 0 {
		t.Fatal("deleted EMA retained one-hot generation state")
	}
	recoveredTime := now.Add(6 * time.Second)
	if err := os.WriteFile(path, []byte(emaFixture(recoveredTime, [][2]any{{"a", 0.2}})+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := monitor.processEMAFileAt(now.Add(7 * time.Second)); err != nil || !monitor.lastEMATime.Equal(recoveredTime) {
		t.Fatalf("recreated EMA did not recover: time=%v err=%v", monitor.lastEMATime, err)
	}
	monitor.withdrawEMA(true)
}
