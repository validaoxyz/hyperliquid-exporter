package monitors

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestParseTokioLine_RealSample(t *testing.T) {
	line := []byte(`["2026-05-25T06:59:59.712513828",{"task_name":"gossip rpc request handler","instrumented_count":0,"dropped_count":0,"first_poll_count":0,"total_first_poll_delay":0.0,"total_idled_count":5,"total_idle_duration":53.476108197,"total_scheduled_count":5,"total_scheduled_duration":0.0000577,"total_poll_count":5,"total_poll_duration":0.00048247,"total_fast_poll_count":5,"total_slow_poll_count":0,"total_short_delay_count":5,"total_long_delay_count":0}]`)
	s, ok := parseTokioLine(line)
	if !ok {
		t.Fatalf("parse failed")
	}
	if s.TaskName != "gossip rpc request handler" {
		t.Errorf("task_name = %q", s.TaskName)
	}
	if s.TotalPollCount != 5 || s.TotalSlowPollCount != 0 || s.TotalLongDelayCount != 0 {
		t.Errorf("counts: %+v", s)
	}
	if s.TotalPollDuration != 0.00048247 {
		t.Errorf("poll_duration = %v", s.TotalPollDuration)
	}
}

func TestParseTokioLine_Malformed(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(``),
		[]byte(`["ts"]`),                         // missing inner
		[]byte(`["ts", null]`),                   // inner not object
		[]byte(`["ts", {"task_name": ""}]`),      // empty task_name
		[]byte(`["ts", {"no_task_name": true}]`), // missing field
		[]byte(`["2026-08-08T00:00:00", {"task_name":"gossip rpc request handler","dropped_count":null}]`),
	} {
		if _, ok := parseTokioLine(line); ok {
			t.Errorf("should reject %q", line)
		}
	}
}

func tokioFixture(at time.Time, task string, polls int) string {
	return fmt.Sprintf(`[%q,{"task_name":%q,"dropped_count":0,"total_idle_duration":1,"total_scheduled_count":%d,"total_scheduled_duration":0.1,"total_poll_count":%d,"total_poll_duration":0.2,"total_fast_poll_count":%d,"total_slow_poll_count":0,"total_short_delay_count":%d,"total_long_delay_count":0}]`, at.UTC().Format("2006-01-02T15:04:05.999999999"), task, polls, polls, polls, polls)
}

func TestTokioSnapshotEmptyDeletedRecreatedAndReadFailure(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	hour := filepath.Join(root, now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(hour), 0o755); err != nil {
		t.Fatal(err)
	}
	task := "gossip rpc request handler"
	if err := os.WriteFile(hour, []byte(tokioFixture(now, task, 4)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now, now); err != nil {
		t.Fatal(err)
	}
	state := &tokioMonitorState{publishedTasks: make(map[string]struct{})}
	if err := tickTokio(root, state, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !b03CollectorHasLabels(t, metrics.HLTokioTaskPollsTotal, map[string]string{"task": task}) || len(b03CollectorRows(t, metrics.HLTokioSampleAgeSeconds)) != 1 {
		t.Fatal("nonempty tokio snapshot was not published")
	}

	// A read failure retains the last complete snapshot.
	if err := os.Remove(hour); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(hour, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tickTokio(root, state, now.Add(2*time.Second)); err == nil {
		t.Fatal("directory-as-hour unexpectedly read successfully")
	}
	if !b03CollectorHasLabels(t, metrics.HLTokioTaskPollsTotal, map[string]string{"task": task}) {
		t.Fatal("transient read error cleared task snapshot")
	}

	if err := os.Remove(hour); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hour, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tickTokio(root, state, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if b03CollectorHasLabels(t, metrics.HLTokioTaskPollsTotal, map[string]string{"task": task}) || len(b03CollectorRows(t, metrics.HLTokioSampleAgeSeconds)) != 0 {
		t.Fatal("successful empty tokio snapshot retained task or age")
	}
	if b03GatherHasFamily(t, "hl_tokio_sample_age_seconds") {
		t.Fatal("successful empty tokio snapshot remained in gathered exposition")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := tickTokio(root, state, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(b03CollectorRows(t, metrics.HLTokioSampleAgeSeconds)) != 0 {
		t.Fatal("deleted tokio tree retained age")
	}

	if err := os.MkdirAll(filepath.Dir(hour), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hour, []byte(tokioFixture(now.Add(5*time.Second), task, 8)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now.Add(5*time.Second), now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tickTokio(root, state, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLTokioTaskPollsTotal, map[string]string{"task": task}); !ok || value != 8 {
		t.Fatalf("recreated tokio polls = %v, %v", value, ok)
	}
	withdrawTokioSnapshot(state, true)
}
