package monitors

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func newOptionalStreamStates() map[string]*optionalStreamState {
	states := make(map[string]*optionalStreamState, len(optionalStreamDirs))
	for _, stream := range optionalStreamDirs {
		states[stream.name] = &optionalStreamState{}
	}
	return states
}

func TestOptionalStreamLifecycleAndFixedAllowlist(t *testing.T) {
	dataRoot := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	states := newOptionalStreamStates()
	stream := "node_fills_streaming"

	tickOptionalStreams(dataRoot, states, now)
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}) {
		t.Fatal("never-seen optional stream emitted an age")
	}
	emptyRoot := filepath.Join(dataRoot, stream, "hourly")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(time.Second))
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}) {
		t.Fatal("present-empty optional stream emitted an age")
	}

	hour := filepath.Join(emptyRoot, now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(hour), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hour, []byte(`{"record":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now, now); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(2*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}); !ok || value != 2 {
		t.Fatalf("valid optional stream age = %v, %v", value, ok)
	}

	// An incomplete record cannot advance freshness; age continues from the
	// last complete record instead.
	if err := os.WriteFile(hour, []byte(`{"record":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now.Add(3*time.Second), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(5*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}); !ok || value != 5 {
		t.Fatalf("partial record advanced freshness: age=%v, %v", value, ok)
	}

	// encoding/json considers null syntactically valid, but it is not a
	// structured stream record and cannot refresh a fixed source.
	if err := os.WriteFile(hour, []byte("null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now.Add(6*time.Second), now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(7*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}); !ok || value != 7 {
		t.Fatalf("JSON null advanced optional-stream freshness: age=%v, %v", value, ok)
	}

	if err := os.RemoveAll(filepath.Join(dataRoot, stream)); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(8*time.Second))
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}) {
		t.Fatal("deleted optional stream retained age")
	}

	unknown := filepath.Join(dataRoot, "future_unverified_stream", "hourly", now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(unknown), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknown, []byte(`{"record":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(9*time.Second))
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": "future_unverified_stream"}) {
		t.Fatal("arbitrary directory expanded the optional-stream label set")
	}
}
