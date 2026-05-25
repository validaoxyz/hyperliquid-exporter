package monitors

import (
	"testing"
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
	} {
		if _, ok := parseTokioLine(line); ok {
			t.Errorf("should reject %q", line)
		}
	}
}
