package monitors

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestValidatorConnectionResolutionDistinguishesReadableEmptyAndAbsent(t *testing.T) {
	metrics.RegisterSource(metrics.SourceValidatorConnections, true)
	root := filepath.Join(t.TempDir(), "hourly")
	if err := os.MkdirAll(filepath.Join(root, "20260809"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := string(metrics.SourceValidatorConnections)
	discoveryErrors := metrics.HLExporterSourceErrorsTotal.WithLabelValues(source, string(metrics.SourceFailureDiscovery))
	before := hostMetricValue(t, discoveryErrors)

	result := resolveValidatorConnectionStream(root)
	if result.err != nil || result.path != "" || result.unavailable != tailStreamUnavailableEmpty {
		t.Fatalf("readable empty result = %+v", result)
	}
	markValidatorConnectionUnavailable(result.unavailable)
	markValidatorConnectionUnavailable(result.unavailable)
	metrics.PublishMonitorHealthSnapshot()
	assertValidatorConnectionSourceState(t, 1, 1, 1)
	assertValidatorConnectionNoObservation(t)
	if got := hostMetricValue(t, discoveryErrors); got != before {
		t.Fatalf("readable empty tree incremented discovery errors: before=%v after=%v", before, got)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	result = resolveValidatorConnectionStream(missing)
	if result.err != nil || result.path != "" || result.unavailable != tailStreamUnavailableAbsent {
		t.Fatalf("missing result = %+v", result)
	}
	markValidatorConnectionUnavailable(result.unavailable)
	metrics.PublishMonitorHealthSnapshot()
	assertValidatorConnectionSourceState(t, 0, -1, -1)
	assertValidatorConnectionNoObservation(t)
	if got := hostMetricValue(t, discoveryErrors); got != before {
		t.Fatalf("missing tree incremented discovery errors: before=%v after=%v", before, got)
	}

	// A nested traversal error is uncertainty, never a valid empty tree. Keep
	// the last presence result while failing read/schema and counting one
	// bounded discovery error.
	markValidatorConnectionUnavailable(tailStreamUnavailableEmpty)
	wantErr := errors.New("injected nested traversal failure")
	result = resolveValidatorConnectionStreamWith(root, func(path string) ([]os.DirEntry, error) {
		if path == root {
			return os.ReadDir(path)
		}
		return nil, wantErr
	})
	if !errors.Is(result.err, wantErr) || result.path != "" || result.unavailable != "" {
		t.Fatalf("nested traversal result = %+v", result)
	}
	metrics.PublishMonitorHealthSnapshot()
	assertValidatorConnectionSourceState(t, 1, 0, -1)
	assertValidatorConnectionNoObservation(t)
	if got := hostMetricValue(t, discoveryErrors); got != before+1 {
		t.Fatalf("nested traversal discovery errors=%v, want %v", got, before+1)
	}
}

func assertValidatorConnectionNoObservation(t *testing.T) {
	t.Helper()
	source := string(metrics.SourceValidatorConnections)
	if got := hostMetricValue(t, metrics.HLExporterSourceEverObserved.WithLabelValues(source)); got != 0 {
		t.Fatalf("source_ever_observed=%v, want 0", got)
	}
	labels := map[string]string{"source": source}
	if b03CollectorHasLabels(t, metrics.HLExporterSourceLastValidSeconds, labels) {
		t.Fatal("non-observation state published a last-valid clock")
	}
	if b03CollectorHasLabels(t, metrics.HLExporterSourceLastPublicationSeconds, labels) {
		t.Fatal("non-observation state published a last-publication clock")
	}
}

func assertValidatorConnectionSourceState(t *testing.T, present, readOK, schemaOK float64) {
	t.Helper()
	source := string(metrics.SourceValidatorConnections)
	if got := hostMetricValue(t, metrics.HLExporterSourcePresent.WithLabelValues(source)); got != present {
		t.Fatalf("source_present=%v, want %v", got, present)
	}
	if got := hostMetricValue(t, metrics.HLExporterSourceReadOK.WithLabelValues(source)); got != readOK {
		t.Fatalf("source_read_ok=%v, want %v", got, readOK)
	}
	if got := hostMetricValue(t, metrics.HLExporterSourceSchemaOK.WithLabelValues(source)); got != schemaOK {
		t.Fatalf("source_schema_ok=%v, want %v", got, schemaOK)
	}
}
