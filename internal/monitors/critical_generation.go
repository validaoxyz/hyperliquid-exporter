package monitors

import (
	"sync"
	"time"
)

const (
	critMsgStaleAfter     = 15 * time.Minute
	critMsgGenerationSkew = 2 * time.Minute
)

type critGeneration struct {
	Source     string
	SampleTime time.Time
	BaseTime   time.Time
	NBugs      int64
	NCrits     int64
	NLocations int64
}

func (g critGeneration) same(other critGeneration) bool {
	return g.Source == other.Source &&
		g.SampleTime.Equal(other.SampleTime) &&
		g.BaseTime.Equal(other.BaseTime) &&
		g.NBugs == other.NBugs &&
		g.NCrits == other.NCrits &&
		g.NLocations == other.NLocations
}

type critDailyProjection struct {
	generation critGeneration
	available  bool
}

type critGenerationStore struct {
	mu      sync.RWMutex
	sources map[string]critDailyProjection
}

func newCritGenerationStore() *critGenerationStore {
	return &critGenerationStore{sources: make(map[string]critDailyProjection)}
}

func (s *critGenerationStore) set(source string, projection critDailyProjection) {
	s.mu.Lock()
	s.sources[source] = projection
	s.mu.Unlock()
}

func (s *critGenerationStore) markUnavailable(source string) {
	s.mu.Lock()
	projection := s.sources[source]
	projection.available = false
	s.sources[source] = projection
	s.mu.Unlock()
}

func (s *critGenerationStore) get(source string) (critDailyProjection, bool) {
	s.mu.RLock()
	projection, ok := s.sources[source]
	s.mu.RUnlock()
	return projection, ok
}

// withAvailable serializes a rich-projection match and commit against daily
// generation replacement/withdrawal. The callback runs only while the exact
// daily generation remains available.
func (s *critGenerationStore) withAvailable(source string, fn func(critGeneration) bool) (critGeneration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	projection, ok := s.sources[source]
	if !ok || !projection.available || !fn(projection.generation) {
		return critGeneration{}, false
	}
	return projection.generation, true
}

var sharedCritGenerations = newCritGenerationStore()
