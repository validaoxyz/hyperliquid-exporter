package peerset

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegister_AddsNewAndUpdatesExisting(t *testing.T) {
	s := New(10, time.Hour)
	now := time.Now()

	if ev, evicted := s.Register("192.0.2.1", "in", now); evicted || ev != "" {
		t.Fatalf("first add should not evict: ev=%q evicted=%v", ev, evicted)
	}
	if got := s.Len(); got != 1 {
		t.Errorf("Len=%d want 1", got)
	}
	if got := s.AddedTotal(); got != 1 {
		t.Errorf("AddedTotal=%d want 1", got)
	}

	// Re-register same IP: no new add, no eviction.
	later := now.Add(time.Minute)
	if ev, evicted := s.Register("192.0.2.1", "out", later); evicted || ev != "" {
		t.Errorf("re-register should not evict: ev=%q evicted=%v", ev, evicted)
	}
	if got := s.AddedTotal(); got != 1 {
		t.Errorf("AddedTotal should still be 1 after re-register, got %d", got)
	}
}

func TestRegister_EvictsLeastRecentlySeenAtCap(t *testing.T) {
	s := New(3, time.Hour)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Fill capacity with three peers, each seen one second apart.
	s.Register("192.0.2.1", "in", t0)
	s.Register("192.0.2.2", "in", t0.Add(1*time.Second))
	s.Register("192.0.2.3", "in", t0.Add(2*time.Second))

	// Add a fourth; oldest (192.0.2.1) must be evicted.
	ev, evicted := s.Register("192.0.2.4", "in", t0.Add(3*time.Second))
	if !evicted {
		t.Fatal("expected eviction when at capacity")
	}
	if ev != "192.0.2.1" {
		t.Errorf("evicted=%q want 192.0.2.1", ev)
	}
	if s.Len() != 3 {
		t.Errorf("Len=%d want 3 after eviction", s.Len())
	}
	if s.EvictedTotal() != 1 {
		t.Errorf("EvictedTotal=%d want 1", s.EvictedTotal())
	}
}

func TestEvictExpired_RemovesEntriesOlderThanTTL(t *testing.T) {
	s := New(10, time.Hour)
	now := time.Now()

	s.Register("old", "in", now.Add(-2*time.Hour))
	s.Register("recent", "in", now.Add(-30*time.Minute))
	s.Register("fresh", "in", now)

	evicted := s.EvictExpired(now)
	if len(evicted) != 1 || evicted[0] != "old" {
		t.Errorf("expected only 'old' evicted, got %v", evicted)
	}
	if s.Len() != 2 {
		t.Errorf("Len=%d want 2", s.Len())
	}
	if s.EvictedTotal() != 1 {
		t.Errorf("EvictedTotal=%d want 1", s.EvictedTotal())
	}
}

func TestUniqueSeenIn_WindowedCounts(t *testing.T) {
	s := New(100, 24*time.Hour)
	now := time.Now()

	// 3 peers in last 5 min, 5 peers in last 1h, 10 peers in last 24h.
	for i := range 3 {
		s.Register(ipStr("5m", i), "in", now.Add(-time.Duration(i)*time.Minute))
	}
	for i := range 2 {
		s.Register(ipStr("1h", i), "in", now.Add(-30*time.Minute-time.Duration(i)*time.Minute))
	}
	for i := range 5 {
		s.Register(ipStr("24h", i), "in", now.Add(-6*time.Hour-time.Duration(i)*time.Minute))
	}

	if got := s.UniqueSeenIn(5*time.Minute, now); got != 3 {
		t.Errorf("5m window: got %d want 3", got)
	}
	if got := s.UniqueSeenIn(time.Hour, now); got != 5 {
		t.Errorf("1h window: got %d want 5", got)
	}
	if got := s.UniqueSeenIn(24*time.Hour, now); got != 10 {
		t.Errorf("24h window: got %d want 10", got)
	}
}

func TestRegister_EmptyIPIgnored(t *testing.T) {
	s := New(10, time.Hour)
	if _, evicted := s.Register("", "in", time.Now()); evicted {
		t.Error("empty IP should be a no-op")
	}
	if s.Len() != 0 {
		t.Errorf("Len=%d want 0 after empty register", s.Len())
	}
	if s.AddedTotal() != 0 {
		t.Errorf("AddedTotal=%d want 0", s.AddedTotal())
	}
}

func TestSnapshot_ReturnsCopy(t *testing.T) {
	s := New(10, time.Hour)
	s.Register("192.0.2.1", "in", time.Now())
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot len=%d want 1", len(snap))
	}
	// Mutating the snapshot must not affect the set.
	snap[0].IP = "mutated"
	again := s.Snapshot()
	if again[0].IP != "192.0.2.1" {
		t.Errorf("snapshot mutation leaked into set: got %q", again[0].IP)
	}
}

func TestSortedByLastSeen_DescendingOrder(t *testing.T) {
	s := New(10, time.Hour)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	s.Register("a", "in", t0.Add(time.Second))
	s.Register("b", "in", t0.Add(3*time.Second))
	s.Register("c", "in", t0.Add(2*time.Second))

	sorted := s.SortedByLastSeen()
	want := []string{"b", "c", "a"}
	for i, p := range sorted {
		if p.IP != want[i] {
			t.Errorf("position %d: got %q want %q", i, p.IP, want[i])
		}
	}
}

func ipStr(prefix string, i int) string {
	return fmt.Sprintf("%s.%d", prefix, i)
}

// Regression: Default() must return the same instance across concurrent
// callers. Earlier nil-check version raced when the producer and
// consumer monitors initialised concurrently; each could create its own
// Set, and the producer's Register calls were invisible to the consumer.
func TestDefault_RaceFreeSingleton(t *testing.T) {
	const goroutines = 32
	results := make(chan *Set, goroutines)
	var start sync.WaitGroup
	start.Add(1)
	for range goroutines {
		go func() {
			start.Wait() // pile up at the gate before all calling Default
			results <- Default()
		}()
	}
	start.Done()

	first := <-results
	if first == nil {
		t.Fatal("Default returned nil")
	}
	for i := 1; i < goroutines; i++ {
		if got := <-results; got != first {
			t.Fatalf("goroutine %d got a different instance from goroutine 0", i)
		}
	}
}
