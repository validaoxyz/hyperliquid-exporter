package metrics

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func resetHealthStateForTest(t *testing.T) {
	t.Helper()
	monitorsMu.Lock()
	monitors = map[string]*monitorState{}
	monitorsMu.Unlock()
	sourcesMu.Lock()
	sources = newSourceStateMap()
	sourcesMu.Unlock()
}

func TestMonitorLifecycleSeparatesLaunchWorkAndProgress(t *testing.T) {
	resetHealthStateForTest(t)

	RegisterMonitor("test")
	if Ready() {
		t.Fatal("registered but unstarted monitor must not be ready")
	}

	state := getOrCreate("test")
	markMonitorStartedAt(state, 10)
	markMonitorStartedAt(state, 11)
	if !Ready() {
		t.Fatal("launch readiness should not wait for a data observation")
	}

	markMonitorAttemptAt(state, 20)
	markMonitorValidObservationAt(state, 30)
	markMonitorPublicationAt(state, 40)

	snapshot := snapshotMonitors()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(snapshot))
	}
	got := snapshot[0]
	if !got.Running || got.Workers != 2 {
		t.Fatalf("running/workers = %v/%d, want true/2", got.Running, got.Workers)
	}
	if got.StartedUnix != 10 {
		t.Fatalf("started = %d, want first zero-to-one transition 10", got.StartedUnix)
	}
	if got.LastAttemptUnix != 30 || got.LastValidUnix != 30 || got.LastPublicationUnix != 40 {
		t.Fatalf("progress clocks = attempt:%d valid:%d publication:%d", got.LastAttemptUnix, got.LastValidUnix, got.LastPublicationUnix)
	}

	MarkMonitorStopped("test")
	got = snapshotMonitors()[0]
	if !got.Running || got.Workers != 1 || got.ExitedUnix != 0 {
		t.Fatalf("one surviving worker = running:%v workers:%d exited:%d", got.Running, got.Workers, got.ExitedUnix)
	}

	MarkMonitorStopped("test")
	got = snapshotMonitors()[0]
	if got.Running || got.Workers != 0 || got.ExitedUnix == 0 {
		t.Fatalf("final worker exit = running:%v workers:%d exited:%d", got.Running, got.Workers, got.ExitedUnix)
	}

	// An extra stop must not underflow the worker count.
	MarkMonitorStopped("test")
	if got := snapshotMonitors()[0].Workers; got != 0 {
		t.Fatalf("workers underflowed to %d", got)
	}
}

func TestSourceStateKeepsReceiptFreshnessSeparateFromSourceTime(t *testing.T) {
	resetHealthStateForTest(t)

	if !RegisterSource(SourceValidatorLatencyEMA, true) {
		t.Fatal("allowlisted source rejected")
	}
	if RegisterSource(SourceID("raw/path/from/input"), true) {
		t.Fatal("non-allowlisted source accepted")
	}

	observedAt := time.Unix(1_000, 0)
	futureSourceTime := time.Unix(9_999, 0)
	if !markSourceValidObservationAt(SourceValidatorLatencyEMA, futureSourceTime, observedAt) {
		t.Fatal("valid source observation rejected")
	}
	if !MarkSourcePublication(SourceValidatorLatencyEMA) {
		t.Fatal("source publication rejected")
	}

	snapshot := snapshotSources()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot length = %d, want only the registered source", len(snapshot))
	}
	got := snapshot[0]
	if got.LastValidUnix != observedAt.Unix() || got.SourceTimeUnix != futureSourceTime.Unix() {
		t.Fatalf("receipt/source clocks = %d/%d", got.LastValidUnix, got.SourceTimeUnix)
	}
	if got.Present != 1 || got.ReadOK != 1 || got.SchemaOK != 1 || !got.EverObserved {
		t.Fatalf("valid state = present:%d read:%d schema:%d ever:%v", got.Present, got.ReadOK, got.SchemaOK, got.EverObserved)
	}

	// Health publication derives age from exporter receipt time, not 9999.
	publishSourceHealthAt(1_010)
	metric, err := HLExporterSourceLastValidAgeSeconds.GetMetricWithLabelValues(string(SourceValidatorLatencyEMA))
	if err != nil {
		t.Fatal(err)
	}
	var encoded dto.Metric
	if err := metric.Write(&encoded); err != nil {
		t.Fatal(err)
	}
	if gotAge := encoded.GetGauge().GetValue(); gotAge != 10 {
		t.Fatalf("last-valid age = %v, want 10", gotAge)
	}

	if !MarkSourceAbsent(SourceValidatorLatencyEMA) {
		t.Fatal("source absence rejected")
	}
	got = snapshotSources()[0]
	if got.Present != 0 || got.ReadOK != sourceStateUnknown || got.SchemaOK != sourceStateUnknown {
		t.Fatalf("absent state = present:%d read:%d schema:%d", got.Present, got.ReadOK, got.SchemaOK)
	}
	if got.LastValidUnix != observedAt.Unix() {
		t.Fatal("absence erased the last-good receipt timestamp")
	}
}

func TestSourceVocabularyIsUniqueAndFailureStagesAreBounded(t *testing.T) {
	seen := make(map[SourceID]struct{}, len(sourceIDs))
	for _, id := range sourceIDs {
		if id == "" {
			t.Fatal("empty source ID")
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate source ID %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != len(newSourceStateMap()) {
		t.Fatalf("source vocabulary/map sizes differ: %d/%d", len(seen), len(newSourceStateMap()))
	}

	resetHealthStateForTest(t)
	RegisterSource(SourceMempool, true)
	if MarkSourceError(SourceMempool, SourceFailureStage("raw-error-text")) {
		t.Fatal("unbounded failure stage accepted")
	}
	if !MarkSourceError(SourceMempool, SourceFailureDecode) {
		t.Fatal("allowlisted failure stage rejected")
	}
	got := snapshotSources()[0]
	if got.ReadOK != 1 || got.SchemaOK != 0 {
		t.Fatalf("decode failure state = read:%d schema:%d", got.ReadOK, got.SchemaOK)
	}
}

func TestSourceFailureStagesPublishReadAndSchemaState(t *testing.T) {
	readFailures := []SourceFailureStage{
		SourceFailureDiscovery,
		SourceFailureStat,
		SourceFailureOpen,
		SourceFailureRead,
		SourceFailureWalk,
		SourceFailureRequest,
		SourceFailureStatus,
	}
	for _, stage := range readFailures {
		t.Run(string(stage), func(t *testing.T) {
			resetHealthStateForTest(t)
			RegisterSource(SourceMempool, true)
			MarkSourceReadOutcome(SourceMempool, true)
			MarkSourceSchemaOutcome(SourceMempool, true)

			if !MarkSourceError(SourceMempool, stage) {
				t.Fatalf("stage %q was rejected", stage)
			}
			got := snapshotSources()[0]
			if got.ReadOK != 0 || got.SchemaOK != sourceStateUnknown {
				t.Fatalf("stage %q state = read:%d schema:%d, want 0/%d", stage, got.ReadOK, got.SchemaOK, sourceStateUnknown)
			}
		})
	}

	for _, stage := range []SourceFailureStage{SourceFailureDecode, SourceFailureSchema} {
		t.Run(string(stage), func(t *testing.T) {
			resetHealthStateForTest(t)
			RegisterSource(SourceMempool, true)
			if !MarkSourceError(SourceMempool, stage) {
				t.Fatalf("stage %q was rejected", stage)
			}
			got := snapshotSources()[0]
			if got.ReadOK != 1 || got.SchemaOK != 0 {
				t.Fatalf("stage %q state = read:%d schema:%d, want 1/0", stage, got.ReadOK, got.SchemaOK)
			}
		})
	}
}
