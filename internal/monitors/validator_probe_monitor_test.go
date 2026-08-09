package monitors

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestValidatorProbeCycleIsSingleFlight(t *testing.T) {
	validatorProbeCycleMu.Lock()
	if runValidatorProbeCycle(context.Background(), time.Now()) {
		validatorProbeCycleMu.Unlock()
		t.Fatal("overlapping validator probe cycle was admitted")
	}
	validatorProbeCycleMu.Unlock()
	if !runValidatorProbeCycle(context.Background(), time.Now()) {
		t.Fatal("idle validator probe cycle was rejected")
	}
}

func TestValidatorProbeWorkersRecoverPerJobWithoutDrainingPool(t *testing.T) {
	panicCounter := metrics.HLExporterMonitorPanicsTotal.WithLabelValues("validator_ip")
	before := validatorMetricValue(t, panicCounter)
	jobs := make(chan validatorProbeTarget)
	var wg sync.WaitGroup
	var completed atomic.Int32
	for range validatorProbeWorkers {
		startValidatorProbeWorker(context.Background(), jobs, time.Now(), &wg, func(_ context.Context, target validatorProbeTarget, _ time.Time) {
			if target.ip == "panic" {
				panic("synthetic validator probe panic")
			}
			completed.Add(1)
		})
	}
	const panicJobs = validatorProbeWorkers + 3
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for range panicJobs {
			jobs <- validatorProbeTarget{ip: "panic"}
		}
		jobs <- validatorProbeTarget{ip: "success"}
		close(jobs)
	}()
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("probe producer blocked after workers recovered panics")
	}
	wg.Wait()

	if got := validatorMetricValue(t, panicCounter) - before; got != panicJobs {
		t.Fatalf("recovered validator probe panics = %v, want %d", got, panicJobs)
	}
	if got := completed.Load(); got != 1 {
		t.Fatalf("post-panic probe jobs completed = %d, want 1", got)
	}
}

func TestValidatorProbeDeadlineBackoffOutcomesAndExpiry(t *testing.T) {
	validatorProbeMu.Lock()
	oldStates := validatorProbeStates
	validatorProbeStates = make(map[string]*validatorProbeState)
	validatorProbeMu.Unlock()
	oldDial := validatorDialContext
	t.Cleanup(func() {
		validatorDialContext = oldDial
		validatorProbeMu.Lock()
		validatorProbeStates = oldStates
		validatorProbeMu.Unlock()
	})

	deadlineSeen := false
	validatorDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > validatorProbeTimeout+100*time.Millisecond {
			t.Errorf("probe dial lacks target-wide deadline: %v, %v", deadline, ok)
		}
		deadlineSeen = true
		return nil, syscall.ECONNREFUSED
	}
	target := validatorProbeTarget{
		identity: metrics.ValidatorIdentity{
			Validator: "0x1111111111111111111111111111111111111111",
			Signer:    "0x2222222222222222222222222222222222222222",
			Name:      "probe",
			Kind:      "validator",
		},
		ip: "192.0.2.1", moniker: "probe",
	}
	refusedBefore := validatorMetricValue(t, metrics.HLConsensusValidatorTCPConnectOutcomes.WithLabelValues(
		target.identity.Validator, target.identity.Signer, target.identity.Name, "refused"))
	now := time.Now()
	probeValidatorTarget(context.Background(), target, now)
	if !deadlineSeen {
		t.Fatal("probe dial was not attempted")
	}
	validatorProbeMu.Lock()
	state := validatorProbeStates[target.identity.Validator]
	firstNext := state.nextAttempt
	validatorProbeMu.Unlock()
	if got := firstNext.Sub(now); got != 2*validatorProbeInterval {
		t.Fatalf("first failure backoff = %v, want %v", got, 2*validatorProbeInterval)
	}

	probeValidatorTarget(context.Background(), target, firstNext)
	validatorProbeMu.Lock()
	secondNext := state.nextAttempt
	state.lastSuccess = now.Add(-validatorProbeValueExpiry - time.Second)
	state.duration = 25 * time.Millisecond
	validatorProbeMu.Unlock()
	if got := secondNext.Sub(firstNext); got != 4*validatorProbeInterval {
		t.Fatalf("second failure backoff = %v, want %v", got, 4*validatorProbeInterval)
	}
	if got := validatorMetricValue(t, metrics.HLConsensusValidatorTCPConnectOutcomes.WithLabelValues(
		target.identity.Validator, target.identity.Signer, target.identity.Name, "refused")) - refusedBefore; got != 2 {
		t.Fatalf("refused outcome delta = %v, want 2", got)
	}

	metrics.SetTCPConnectSuccess(target.identity, state.duration, state.lastSuccess)
	probeValidatorTarget(context.Background(), target, secondNext)
	if validatorCollectorHasLabels(metrics.HLConsensusValidatorTCPConnectDuration, map[string]string{
		"validator": target.identity.Validator, "signer": target.identity.Signer, "name": target.identity.Name,
	}) {
		t.Fatal("expired current TCP-connect duration was not withdrawn after failure")
	}
}

func TestClassifyValidatorConnectErrorsUsesClosedOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"success":     {nil, "success"},
		"timeout":     {context.DeadlineExceeded, "timeout"},
		"refused":     {syscall.ECONNREFUSED, "refused"},
		"unreachable": {syscall.EHOSTUNREACH, "unreachable"},
		"other":       {errors.New("synthetic"), "other"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyConnectError(tc.err); got != tc.want {
				t.Fatalf("classifyConnectError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
