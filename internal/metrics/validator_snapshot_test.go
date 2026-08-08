package metrics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestReplaceValidatorSnapshotPublishesOneIdentityAndAggregateGeneration(t *testing.T) {
	resetValidatorSnapshotTestState(t)

	first := []ValidatorSummarySnapshot{
		{Validator: "0xval1", Signer: "0xsig1", Name: "one", Stake: 10, Active: true},
		{Validator: "0xval2", Signer: "0xsig2", Name: "two", Stake: 20, Active: true, Jailed: true},
		{Validator: "0xval3", Signer: "0xsig3", Name: "three", Stake: 30},
	}
	ReplaceValidatorSnapshot(first)

	assertValidatorSnapshotInvariant(t)
	if identity, ok := ResolveValidatorIdentity("0xsig1"); !ok || identity.Validator != "0xval1" || identity.Kind != "signer" {
		t.Fatalf("signer identity = %+v, %v", identity, ok)
	}

	ReplaceValidatorSnapshot([]ValidatorSummarySnapshot{
		{Validator: "0xval4", Signer: "0xsig4", Name: "four", Stake: 7, Active: true},
	})
	assertValidatorSnapshotInvariant(t)
	if _, ok := ResolveValidatorIdentity("0xsig1"); ok {
		t.Fatal("departed signer survived complete registry replacement")
	}
}

func TestResolveSignerSnapshotKeepsSignerKindForUnknownRows(t *testing.T) {
	resetValidatorSnapshotTestState(t)
	knownSigner := "0x2222222222222222222222222222222222222222"
	unknownSigner := "0x3333333333333333333333333333333333333333"
	ReplaceValidatorSnapshot([]ValidatorSummarySnapshot{{
		Validator: "0x1111111111111111111111111111111111111111",
		Signer:    knownSigner,
		Name:      "known",
		Active:    true,
	}})

	got := ResolveSignerSnapshot([]string{knownSigner, unknownSigner})
	if known := got[knownSigner]; known.Kind != "signer" || known.Validator != "0x1111111111111111111111111111111111111111" || known.Name != "known" {
		t.Fatalf("known signer = %+v", known)
	}
	if unknown := got[unknownSigner]; unknown.Kind != "signer" || unknown.Validator != "unknown" || unknown.Signer != unknownSigner || unknown.Name != "unknown" {
		t.Fatalf("unknown signer lost source kind: %+v", unknown)
	}
}

func TestVoteUnknownSeriesReconcilesFromPreservedWireIdentity(t *testing.T) {
	resetValidatorSnapshotTestState(t)
	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	testMeter := provider.Meter("validator-vote-reconcile-test")
	roundGauge, err := testMeter.Int64ObservableGauge("test_vote_round")
	if err != nil {
		t.Fatal(err)
	}
	ageGauge, err := testMeter.Float64ObservableGauge("test_vote_age")
	if err != nil {
		t.Fatal(err)
	}
	oldRound, oldAge := HLConsensusVoteRoundGauge, HLConsensusVoteTimeDiffGauge
	HLConsensusVoteRoundGauge, HLConsensusVoteTimeDiffGauge = roundGauge, ageGauge
	t.Cleanup(func() {
		metricsMutex.Lock()
		delete(labeledValues, roundGauge)
		delete(labeledValues, ageGauge)
		metricsMutex.Unlock()
		HLConsensusVoteRoundGauge, HLConsensusVoteTimeDiffGauge = oldRound, oldAge
	})

	validator := "0x1111111111111111111111111111111111111111"
	signer := "0x2222222222222222222222222222222222222222"
	truncatedSigner := "0x2222..2222"
	SetValidatorLastVoteRound(truncatedSigner, 77)
	SetValidatorLastVoteTime(truncatedSigner, time.Unix(100, 0))
	RegisterFullAddress(signer)
	ReplaceValidatorSnapshot([]ValidatorSummarySnapshot{{Validator: validator, Signer: signer, Name: "joined"}})

	metricsMutex.RLock()
	round, roundOK := labeledValues[roundGauge][validator]
	age, ageOK := labeledValues[ageGauge][validator]
	metricsMutex.RUnlock()
	if !roundOK || !ageOK || round.value != 77 || age.value != 100 {
		t.Fatalf("reconciled vote rows: round=%+v/%v age=%+v/%v", round, roundOK, age, ageOK)
	}
	if got := labelValue(round.labels, "signer"); got != signer {
		t.Fatalf("reconciled signer = %q, want %q", got, signer)
	}
}

func TestReplaceConsensusStatusSnapshotKeepsDistinctUnknownSignersAndWithdraws(t *testing.T) {
	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	testMeter := provider.Meter("validator-status-reconcile-test")
	heartbeatGauge, _ := testMeter.Float64ObservableGauge("test_heartbeat")
	ackGauge, _ := testMeter.Float64ObservableGauge("test_ack")
	connectivityGauge, _ := testMeter.Float64ObservableGauge("test_connectivity")
	disconnectGauge, _ := testMeter.Int64ObservableGauge("test_disconnect")
	oldHeartbeat, oldAck := HLConsensusHeartbeatStatusGauge, HLConsensusHeartbeatAckObservedGauge
	oldConnectivity, oldDisconnect := HLConsensusConnectivityGauge, HLConsensusDisconnectedSinceRoundGauge
	HLConsensusHeartbeatStatusGauge, HLConsensusHeartbeatAckObservedGauge = heartbeatGauge, ackGauge
	HLConsensusConnectivityGauge, HLConsensusDisconnectedSinceRoundGauge = connectivityGauge, disconnectGauge
	t.Cleanup(func() {
		metricsMutex.Lock()
		delete(labeledValues, heartbeatGauge)
		delete(labeledValues, ackGauge)
		delete(labeledValues, connectivityGauge)
		delete(labeledValues, disconnectGauge)
		metricsMutex.Unlock()
		HLConsensusHeartbeatStatusGauge, HLConsensusHeartbeatAckObservedGauge = oldHeartbeat, oldAck
		HLConsensusConnectivityGauge, HLConsensusDisconnectedSinceRoundGauge = oldConnectivity, oldDisconnect
	})

	one, two := 1.0, 2.0
	signerOne := "0x1111111111111111111111111111111111111111"
	signerTwo := "0x2222222222222222222222222222222222222222"
	unknownOne := ValidatorIdentity{Validator: "unknown", Signer: signerOne, Name: "unknown", Kind: "signer"}
	unknownTwo := ValidatorIdentity{Validator: "unknown", Signer: signerTwo, Name: "unknown", Kind: "signer"}
	ReplaceConsensusStatusSnapshot([]ValidatorHeartbeatSnapshot{
		{Identity: unknownOne, SinceLastSuccess: &one, LastAckDuration: &one, AckFieldPresent: true},
		{Identity: unknownTwo, SinceLastSuccess: &two, LastAckDuration: &two, AckFieldPresent: true},
	}, []ValidatorDisconnectSnapshot{{Subject: unknownOne, Reporter: unknownTwo, SinceRound: 9}})

	metricsMutex.RLock()
	heartbeatCount := len(labeledValues[heartbeatGauge])
	ackCount := len(labeledValues[ackGauge])
	connectivityCount := len(labeledValues[connectivityGauge])
	metricsMutex.RUnlock()
	if heartbeatCount != 4 || ackCount != 2 || connectivityCount != 1 {
		t.Fatalf("status cardinality: heartbeat=%d ack=%d connectivity=%d", heartbeatCount, ackCount, connectivityCount)
	}

	ReplaceConsensusStatusSnapshot([]ValidatorHeartbeatSnapshot{{
		Identity: unknownOne, SinceLastSuccess: &two, AckFieldPresent: true,
	}}, nil)
	metricsMutex.RLock()
	heartbeatRows := labeledValues[heartbeatGauge]
	ackRows := labeledValues[ackGauge]
	connectivityCount = len(labeledValues[connectivityGauge])
	metricsMutex.RUnlock()
	if len(heartbeatRows) != 1 || len(ackRows) != 1 || connectivityCount != 0 {
		t.Fatalf("withdrawn status cardinality: heartbeat=%d ack=%d connectivity=%d", len(heartbeatRows), len(ackRows), connectivityCount)
	}
	if ackRows[signerOne].value != 0 {
		t.Fatalf("explicit null ack observed = %v, want 0", ackRows[signerOne].value)
	}
}

func labelValue(labels []attribute.KeyValue, key string) string {
	for _, label := range labels {
		if string(label.Key) == key {
			return label.Value.AsString()
		}
	}
	return ""
}

func TestReplaceValidatorSnapshotConcurrentReadersNeverSeeSkew(t *testing.T) {
	resetValidatorSnapshotTestState(t)
	a := []ValidatorSummarySnapshot{
		{Validator: "a1", Signer: "as1", Stake: 2, Active: true},
		{Validator: "a2", Signer: "as2", Stake: 3, Jailed: true},
	}
	b := []ValidatorSummarySnapshot{
		{Validator: "b1", Signer: "bs1", Stake: 11, Active: true},
		{Validator: "b2", Signer: "bs2", Stake: 13, Active: true, Jailed: true},
		{Validator: "b3", Signer: "bs3", Stake: 17},
	}

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; i < 2_000; i++ {
			if i%2 == 0 {
				ReplaceValidatorSnapshot(a)
			} else {
				ReplaceValidatorSnapshot(b)
			}
		}
	}()

	for {
		select {
		case <-done:
			assertValidatorSnapshotInvariant(t)
			return
		default:
			if err := validatorSnapshotInvariantError(); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
			select {
			case err := <-errCh:
				t.Fatal(err)
			default:
			}
		}
	}
}

func assertValidatorSnapshotInvariant(t *testing.T) {
	t.Helper()
	if err := validatorSnapshotInvariantError(); err != nil {
		t.Fatal(err)
	}
}

func validatorSnapshotInvariantError() error {
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()
	var total, jailed, notJailed, active, inactive, eligibleStake float64
	var eligibleCount int64
	for _, row := range validatorRegistry.validatorInfo {
		total += row.Stake
		if row.Jailed {
			jailed += row.Stake
		} else {
			notJailed += row.Stake
		}
		if row.Active {
			active += row.Stake
		} else {
			inactive += row.Stake
		}
		if row.Active && !row.Jailed {
			eligibleStake += row.Stake
			eligibleCount++
		}
	}
	want := ValidatorAggregateSnapshot{
		TotalStake: total, JailedStake: jailed, NotJailedStake: notJailed,
		ActiveStake: active, InactiveStake: inactive,
		ValidatorCount:         int64(len(validatorRegistry.validatorInfo)),
		ActiveAndUnjailedStake: eligibleStake, ActiveAndUnjailedCount: eligibleCount,
	}
	got := validatorSnapshotAggregates
	if got.TotalStake != want.TotalStake || got.JailedStake != want.JailedStake ||
		got.NotJailedStake != want.NotJailedStake || got.ActiveStake != want.ActiveStake ||
		got.InactiveStake != want.InactiveStake || got.ValidatorCount != want.ValidatorCount ||
		got.ActiveAndUnjailedStake != want.ActiveAndUnjailedStake || got.ActiveAndUnjailedCount != want.ActiveAndUnjailedCount {
		return fmt.Errorf("row/aggregate generation skew: rows=%+v aggregates=%+v", want, got)
	}
	return nil
}

func resetValidatorSnapshotTestState(t *testing.T) {
	t.Helper()
	metricsMutex.Lock()
	oldRegistry := validatorRegistry
	oldGeneration := validatorSnapshotGeneration
	oldAggregates := validatorSnapshotAggregates
	validatorRegistry.signerToValidator = make(map[string]string)
	validatorRegistry.validatorInfo = make(map[string]ValidatorInfo)
	validatorSnapshotGeneration = 0
	validatorSnapshotAggregates = ValidatorAggregateSnapshot{}
	metricsMutex.Unlock()
	t.Cleanup(func() {
		metricsMutex.Lock()
		validatorRegistry = oldRegistry
		validatorSnapshotGeneration = oldGeneration
		validatorSnapshotAggregates = oldAggregates
		metricsMutex.Unlock()
	})
}
