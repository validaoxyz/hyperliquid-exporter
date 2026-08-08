// Package peerset maintains a thread-safe, LRU+TTL-bounded set of peer
// IPs the node has observed. Fed by monitors that parse per-peer
// information (currently tcp_traffic_monitor); read by peer_set_monitor
// for windowed-unique-count and per-IP metrics.
package peerset

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Peer is one entry in the set.
type Peer struct {
	IP        string
	FirstSeen time.Time
	LastSeen  time.Time
}

// Set is the bounded peer-tracking data structure.
type Set struct {
	mu    sync.RWMutex
	peers map[string]*Peer
	cap   int
	ttl   time.Duration

	addedTotal atomic.Uint64
	evictedTTL atomic.Uint64
	evictedCap atomic.Uint64
}

// New builds an empty set with the given LRU cap and TTL.
func New(cap int, ttl time.Duration) *Set {
	return &Set{
		peers: make(map[string]*Peer, cap),
		cap:   cap,
		ttl:   ttl,
	}
}

// defaultSet is the package-global instance the monitors share.
// Constructed exactly once via defaultOnce. Earlier versions used a
// nil-check, which raced when the tcp_traffic producer and peer_set
// publisher first called Default concurrently: each goroutine could
// see defaultSet==nil and create its own Set. Producer's Register
// calls then went to one instance while the publisher read from the
// other.
var (
	defaultSet  *Set
	defaultOnce sync.Once
)

// Default returns the package-global set with sensible defaults:
// 2048 peers, 24h TTL. Cap chosen so the rolling 24h unique count
// stays accurate on nodes with heavy peer churn while still bounding
// optional per-IP metric cardinality.
func Default() *Set {
	defaultOnce.Do(func() {
		defaultSet = New(2048, 24*time.Hour)
	})
	return defaultSet
}

// Register records a qualified traffic-endpoint observation at now. The
// direction argument is retained for source compatibility but deliberately
// ignored: registry membership is not a directional or connectivity claim.
// Returns (evictedIP, true) if the set
// was at capacity and a peer was bumped to make room.
func (s *Set) Register(ip, _ string, now time.Time) (string, bool) {
	if ip == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce TTL before either refreshing an existing entry or deciding that
	// the set is at capacity. Otherwise a producer tick that wins scheduling
	// just before the periodic publisher can revive an expired endpoint, or
	// misclassify its removal as a capacity eviction.
	s.evictExpiredLocked(now)

	if p, ok := s.peers[ip]; ok {
		p.LastSeen = now
		return "", false
	}

	var evicted string
	if len(s.peers) >= s.cap {
		evicted = s.evictOldestLocked()
	}
	s.peers[ip] = &Peer{
		IP:        ip,
		FirstSeen: now,
		LastSeen:  now,
	}
	s.addedTotal.Add(1)
	return evicted, evicted != ""
}

// EvictExpired drops peers whose LastSeen is older than now - ttl.
// Returns the evicted IPs so callers can clean per-IP metric series.
func (s *Set) EvictExpired(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(now)
}

// evictExpiredLocked applies the TTL boundary and accounts every removal as a
// TTL eviction. The caller must hold s.mu.
func (s *Set) evictExpiredLocked(now time.Time) []string {
	cutoff := now.Add(-s.ttl)
	var evicted []string
	for ip, p := range s.peers {
		if p.LastSeen.Before(cutoff) {
			delete(s.peers, ip)
			evicted = append(evicted, ip)
		}
	}
	sort.Strings(evicted)
	s.evictedTTL.Add(uint64(len(evicted)))
	return evicted
}

// UniqueSeenIn returns the count of peers whose LastSeen is within d
// of now.
func (s *Set) UniqueSeenIn(d time.Duration, now time.Time) int {
	cutoff := now.Add(-d)
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, p := range s.peers {
		if !p.LastSeen.Before(cutoff) {
			n++
		}
	}
	return n
}

// Len returns the current size of the set.
func (s *Set) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers)
}

// AddedTotal returns the cumulative count of Register calls that added
// a new peer (vs updating an existing one).
func (s *Set) AddedTotal() uint64 { return s.addedTotal.Load() }

// EvictedTotal returns the cumulative count of evictions (both LRU and
// TTL-driven).
func (s *Set) EvictedTotal() uint64 { return s.evictedTTL.Load() + s.evictedCap.Load() }

// EvictedByReason returns the monotonic process-local eviction total for one
// fixed reason. Unknown reasons return zero.
func (s *Set) EvictedByReason(reason string) uint64 {
	switch reason {
	case "ttl":
		return s.evictedTTL.Load()
	case "capacity":
		return s.evictedCap.Load()
	default:
		return 0
	}
}

// Snapshot returns a copy of every peer. Order is not specified.
// Callers must treat the returned slice as read-only.
func (s *Set) Snapshot() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, Peer{
			IP:        p.IP,
			FirstSeen: p.FirstSeen,
			LastSeen:  p.LastSeen,
		})
	}
	return out
}

// evictOldestLocked drops the peer with the oldest LastSeen. Returns
// the evicted IP. Caller must hold s.mu.
func (s *Set) evictOldestLocked() string {
	if len(s.peers) == 0 {
		return ""
	}
	// Small set (≤cap), linear scan is fine.
	var oldestIP string
	var oldestTime time.Time
	first := true
	for ip, p := range s.peers {
		if first || p.LastSeen.Before(oldestTime) || (p.LastSeen.Equal(oldestTime) && ip < oldestIP) {
			oldestIP = ip
			oldestTime = p.LastSeen
			first = false
		}
	}
	delete(s.peers, oldestIP)
	s.evictedCap.Add(1)
	return oldestIP
}

// SortedByLastSeen returns peers ordered by LastSeen descending. Used
// for stable per-peer metric publishing when --per-peer-metrics is on.
func (s *Set) SortedByLastSeen() []Peer {
	out := s.Snapshot()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].IP < out[j].IP
	})
	return out
}
