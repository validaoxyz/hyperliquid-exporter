package monitors

import (
	"testing"
	"time"
)

func setSharedTrafficForTest(source, receipt time.Time, inbound []peerSample) {
	sharedTCPTraffic.mu.Lock()
	sharedTCPTraffic.timestamp = source
	sharedTCPTraffic.receivedAt = receipt
	sharedTCPTraffic.inbound = append([]peerSample(nil), inbound...)
	sharedTCPTraffic.outbound = nil
	sharedTCPTraffic.mu.Unlock()
}

func TestRankInboundEWMA_DeterministicTie(t *testing.T) {
	ranked := rankInboundEWMA(map[string]float64{"192.0.2.2": 1, "192.0.2.1": 1})
	if len(ranked) != 2 || ranked[0].ip != "192.0.2.1" {
		t.Fatalf("ranked=%+v", ranked)
	}
}

func TestDominantInbound_StrictHysteresisClearAndRecovery(t *testing.T) {
	state := newDominantInboundState()
	t0 := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	setSharedTrafficForTest(t0, t0, []peerSample{{ip: "192.0.2.2", value: 10}, {ip: "192.0.2.1", value: 10}})
	tickDominantInbound(t0, state)
	if state.current != "192.0.2.1" {
		t.Fatalf("initial tie candidate=%q", state.current)
	}

	// The resulting EWMAs are A=5.1 and B=6.12, exactly 1.2x. Strict
	// hysteresis retains A.
	t1 := t0.Add(30 * time.Second)
	setSharedTrafficForTest(t1, t1, []peerSample{{ip: "192.0.2.1", value: 10}, {ip: "192.0.2.2", value: 13.4}})
	tickDominantInbound(t1, state)
	if state.current != "192.0.2.1" {
		t.Fatalf("exactly 1.2 switched candidate to %q", state.current)
	}

	t2 := t1.Add(30 * time.Second)
	setSharedTrafficForTest(t2, t2, []peerSample{{ip: "192.0.2.1", value: 10}, {ip: "192.0.2.2", value: 13.41}})
	tickDominantInbound(t2, state)
	if state.current != "192.0.2.2" {
		t.Fatalf("strictly above 1.2 did not switch: %q", state.current)
	}

	t3 := t2.Add(30 * time.Second)
	setSharedTrafficForTest(t3, t3, []peerSample{{ip: "192.0.2.1", value: 0}})
	tickDominantInbound(t3, state)
	if state.current != "" || len(state.ewma) != 0 {
		t.Fatalf("all-zero snapshot did not clear: current=%q ewma=%v", state.current, state.ewma)
	}

	t4 := t3.Add(30 * time.Second)
	setSharedTrafficForTest(t4, t4, []peerSample{{ip: "192.0.2.9", value: 1}})
	tickDominantInbound(t4, state)
	if state.current != "192.0.2.9" {
		t.Fatalf("recovery did not start a new epoch: %q", state.current)
	}
	tickDominantInbound(t4.Add(91*time.Second), state)
	if state.current != "" || len(state.ewma) != 0 {
		t.Fatalf("stale state did not clear: current=%q ewma=%v", state.current, state.ewma)
	}
}
