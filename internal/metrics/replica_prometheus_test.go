package metrics

import (
	"context"
	"testing"

	api "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestWithdrawReplicaCurrentSnapshotDeletesAndRecreatesCurrentState(t *testing.T) {
	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	testMeter := provider.Meter("replica-withdraw-test")
	round, _ := testMeter.Int64ObservableGauge("test_replica_round")
	timestamp, _ := testMeter.Int64ObservableGauge("test_replica_time")
	parseDuration, _ := testMeter.Float64ObservableGauge("test_replica_parse_duration")

	oldRound, oldTimestamp, oldParseDuration := HLCoreLastProcessedRound, HLCoreLastProcessedTime, HLMetalParseDurationGauge
	HLCoreLastProcessedRound, HLCoreLastProcessedTime, HLMetalParseDurationGauge = round, timestamp, parseDuration
	HLReplicaLastProcessedHeight.Reset()
	t.Cleanup(func() {
		metricsMutex.Lock()
		for _, instrument := range []api.Observable{round, timestamp, parseDuration} {
			delete(currentValues, instrument)
			delete(labeledValues, instrument)
		}
		metricsMutex.Unlock()
		HLCoreLastProcessedRound, HLCoreLastProcessedTime, HLMetalParseDurationGauge = oldRound, oldTimestamp, oldParseDuration
		HLReplicaLastProcessedHeight.Reset()
	})

	SetCoreLastProcessedRound(11)
	SetCoreLastProcessedTime(22)
	SetReplicaParseDuration(0.25)
	SetReplicaLastProcessedHeight(33)
	if rows := metricCollectorRows(HLReplicaLastProcessedHeight); rows != 1 {
		t.Fatalf("height rows before withdrawal = %d, want 1", rows)
	}

	WithdrawReplicaCurrentSnapshot()
	metricsMutex.RLock()
	for name, instrument := range map[string]api.Observable{
		"round": round, "time": timestamp, "parse duration": parseDuration,
	} {
		if _, exists := currentValues[instrument]; exists {
			metricsMutex.RUnlock()
			t.Fatalf("withdrawal retained current %s", name)
		}
	}
	metricsMutex.RUnlock()
	if rows := metricCollectorRows(HLReplicaLastProcessedHeight); rows != 0 {
		t.Fatalf("height rows after withdrawal = %d, want 0", rows)
	}

	SetCoreLastProcessedRound(44)
	SetCoreLastProcessedTime(55)
	SetReplicaParseDuration(0.5)
	SetReplicaLastProcessedHeight(66)
	if rows := metricCollectorRows(HLReplicaLastProcessedHeight); rows != 1 {
		t.Fatalf("height rows after recovery = %d, want 1", rows)
	}
}
