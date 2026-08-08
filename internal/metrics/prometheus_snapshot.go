package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// prometheusSnapshotMu is the publication barrier for metric families that
// describe one logical source generation but use several Prometheus
// collectors. Production scrapes hold a read lock while collectors are
// gathered; a generation commit or withdrawal holds the write lock across all
// of its mutations. A scrape therefore sees the complete old generation or
// the complete new generation, never a mixture.
var prometheusSnapshotMu sync.RWMutex

// WithPrometheusSnapshotUpdate publishes one logical metric generation.
// Keep fn bounded to in-memory state/collector mutations: filesystem and
// network work must finish before entering the barrier.
func WithPrometheusSnapshotUpdate(fn func()) {
	prometheusSnapshotMu.Lock()
	defer prometheusSnapshotMu.Unlock()
	fn()
}

type snapshotProtectedGatherer struct {
	delegate prometheus.Gatherer
}

func (g snapshotProtectedGatherer) Gather() ([]*dto.MetricFamily, error) {
	// Refresh in-memory lifecycle/source projections inside the same exclusion
	// window as collection. A scrape cannot otherwise observe a source-specific
	// success gauge alongside the previous 10-second common-health snapshot.
	// The write lock intentionally serializes protected scrapes; collection is
	// bounded by the HTTP handler, and semantic coherence wins over parallel
	// reads of mutable derived gauges.
	prometheusSnapshotMu.Lock()
	defer prometheusSnapshotMu.Unlock()
	publishMonitorHealthSnapshot()
	return g.delegate.Gather()
}

// ProtectPrometheusSnapshots applies the production scrape barrier to a
// gatherer. It is exported for integration tests and alternate internal
// metrics handlers; callers must not bypass it for a public scrape endpoint.
func ProtectPrometheusSnapshots(delegate prometheus.Gatherer) prometheus.Gatherer {
	return snapshotProtectedGatherer{delegate: delegate}
}

func protectPrometheusSnapshots(delegate prometheus.Gatherer) prometheus.Gatherer {
	return ProtectPrometheusSnapshots(delegate)
}
