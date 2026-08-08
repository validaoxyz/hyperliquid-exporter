package monitors

import (
	"os"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestParseGossipConnectionLine_KnownEvents(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"verified gossip rpc",
			`["2026-05-25T06:59:53.606639758",["verified gossip rpc",{"Ip":"203.0.113.75"}]]`,
			"verified_gossip_rpc",
		},
		{
			"performing checks on stream",
			`["2026-05-25T06:59:53.6",["performing checks on stream","203.0.113.1","gossip"]]`,
			"performing_checks_on_stream",
		},
		{
			"error checking connection current",
			`["2026-08-08T03:00:03",["error checking connection","203.0.113.1","connection refused"]]`,
			"error_checking_connection",
		},
		{
			"rejecting gossip stream because max peers reached current",
			`["2026-08-08T03:00:03",["rejecting gossip stream because max peers reached","peer limit",[]]]`,
			"rejecting_gossip_stream_max_peers_reached",
		},
		{
			"got tcp greeting current",
			`["2026-08-08T03:00:03",["got tcp greeting",{"Ip":"203.0.113.2"},true]]`,
			"got_tcp_greeting",
		},
		{
			"error checking connection historical",
			`["2026-05-25T06:59:53.6",["error checking connection",{"err":"x"}]]`,
			"error_checking_connection",
		},
		{
			"rejecting gossip stream because max peers reached historical",
			`["2026-05-25T06:59:53.6",["rejecting gossip stream because max peers reached",{}]]`,
			"rejecting_gossip_stream_max_peers_reached",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseGossipConnectionLine([]byte(c.line))
			if !ok {
				t.Fatalf("expected ok=true on %q", c.line)
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestGossipRecordFailurePreservesReadableSourceState(t *testing.T) {
	metrics.RegisterSource(metrics.SourceGossipConnections, true)
	resolveOK := true
	metrics.HLP2PGossipSourceUp.Set(0)
	markGossipConnectionTailFailure(tailStreamFailureRecord, &resolveOK)
	if !resolveOK {
		t.Fatal("schema failure cleared successful source resolution")
	}
	var metric dto.Metric
	if err := metrics.HLP2PGossipSourceUp.Write(&metric); err != nil {
		t.Fatal(err)
	}
	if metric.GetGauge().GetValue() != 1 {
		t.Fatalf("source_up=%v, want 1 after record-only failure", metric.GetGauge().GetValue())
	}
	metrics.PublishMonitorHealthSnapshot()
	assertGossipConnectionSourceState(t, 1, 1, 0)
}

func TestGossipConnectionPublicationMalformedHealsThenWithdrawsCoherently(t *testing.T) {
	metrics.RegisterSource(metrics.SourceGossipConnections, true)
	resolveOK := false

	publishGossipConnectionLine(&resolveOK, gossipConnectionParseResult{reason: "json"}, time.Unix(100, 0))
	if !resolveOK {
		t.Fatal("readable malformed record did not heal resolution/readability state")
	}
	if got := hostMetricValue(t, metrics.HLP2PGossipSourceUp); got != 1 {
		t.Fatalf("malformed readable record source_up=%v, want 1", got)
	}
	if got := hostMetricValue(t, metrics.HLP2PGossipLastReadSuccessTimestampSeconds); got != 100 {
		t.Fatalf("malformed readable record last_read_success=%v, want 100", got)
	}
	metrics.PublishMonitorHealthSnapshot()
	assertGossipConnectionSourceState(t, 1, 1, 0)

	valid := parseGossipConnectionRecord([]byte(
		`["2026-08-08T03:00:03",["verified gossip rpc",{"Ip":"192.0.2.1"}]]`,
	))
	if valid.reason != "" {
		t.Fatalf("valid fixture rejected: reason=%q", valid.reason)
	}
	publishGossipConnectionLine(&resolveOK, valid, time.Unix(200, 0))
	metrics.PublishMonitorHealthSnapshot()
	assertGossipConnectionSourceState(t, 1, 1, 1)
	if got := hostMetricValue(t, metrics.HLP2PGossipLastReadSuccessTimestampSeconds); got != 200 {
		t.Fatalf("valid record last_read_success=%v, want 200", got)
	}

	publishGossipConnectionResolution(&resolveOK, os.ErrNotExist)
	if resolveOK {
		t.Fatal("confirmed absence left resolution marked successful")
	}
	if got := hostMetricValue(t, metrics.HLP2PGossipSourceUp); got != 0 {
		t.Fatalf("confirmed absence source_up=%v, want 0", got)
	}
	metrics.PublishMonitorHealthSnapshot()
	assertGossipConnectionSourceState(t, 0, -1, -1)
}

func assertGossipConnectionSourceState(t *testing.T, present, readOK, schemaOK float64) {
	t.Helper()
	source := string(metrics.SourceGossipConnections)
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

func TestParseGossipConnectionLine_AllExactAddedTags(t *testing.T) {
	serveTags := map[string]string{
		"closing gossip stream because no quorum yet": "closing_gossip_stream_no_quorum_yet",
		"finished checks":                                         "finished_checks",
		"sending abci_state":                                      "sending_abci_state",
		"successfully sent abci_state":                            "successfully_sent_abci_state",
		"sending evm kvs":                                         "sending_evm_kvs",
		"dropping connection after sending abci state":            "dropping_connection_after_sending_abci_state",
		"marking node_ip as verified":                             "marking_node_ip_verified",
		"closing gossip stream because peer is already connected": "closing_gossip_stream_peer_already_connected",
	}
	for tag, want := range serveTags {
		line := `["2026-08-08T03:00:03.086327335",["` + tag + `",{"Ip":"192.0.2.1"},true]]`
		got, ok := parseGossipConnectionLine([]byte(line))
		if !ok || got != want {
			t.Errorf("tag=%q got=%q ok=%v want=%q", tag, got, ok, want)
		}
	}
	line := []byte(`["2026-08-08T03:00:03.086327335",["dropping connection",1,2,3,4]]`)
	if got, ok := parseGossipConnectionLine(line); !ok || got != "dropping_connection" {
		t.Fatalf("dropping connection got=%q ok=%v", got, ok)
	}
}

func TestParseGossipConnectionLine_ExactMatchingAndPayloadShape(t *testing.T) {
	for _, tag := range []string{"Sending abci_state", "sending abci_state ", "sending  abci_state"} {
		line := `["2026-08-08T03:00:03",["` + tag + `",{"Ip":"192.0.2.1"},true]]`
		if got, ok := parseGossipConnectionLine([]byte(line)); !ok || got != "other" {
			t.Errorf("near match %q got=%q ok=%v", tag, got, ok)
		}
	}
	badPayload := []byte(`["2026-08-08T03:00:03",["sending abci_state",{"Ip":"192.0.2.1"}]]`)
	result := parseGossipConnectionRecord(badPayload)
	if result.reason != "payload" {
		t.Fatalf("bad payload reason=%q", result.reason)
	}
	badTime := parseGossipConnectionRecord([]byte(`["future-ish",["unknown tag"]]`))
	if badTime.reason != "timestamp" {
		t.Fatalf("bad timestamp reason=%q", badTime.reason)
	}
	nullFlag := parseGossipConnectionRecord([]byte(`["2026-08-08T03:00:03",["sending abci_state",{"Ip":"192.0.2.1"},null]]`))
	if nullFlag.reason != "payload" {
		t.Fatalf("null required flag reason=%q", nullFlag.reason)
	}
	for _, line := range []string{
		`["2026-08-08T03:00:03",["got tcp greeting",{"Ip":"192.0.2.1","extra":true},true]]`,
		`["2026-08-08T03:00:03",["got tcp greeting",{"Ip":"192.0.2.1"},null]]`,
		`["2026-08-08T03:00:03",["rejecting gossip stream because max peers reached","limit",{}]]`,
		`["2026-08-08T03:00:03",["error checking connection","peer",null]]`,
	} {
		if result := parseGossipConnectionRecord([]byte(line)); result.reason != "payload" {
			t.Fatalf("out-of-contract current payload accepted: %s result=%+v", line, result)
		}
	}
}

func TestParseGossipConnectionLine_OtherFallback(t *testing.T) {
	line := []byte(`["2026-05-25T06:59:53.6",["some new event we have not seen yet",{}]]`)
	got, ok := parseGossipConnectionLine(line)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got != "other" {
		t.Errorf("expected 'other', got %q", got)
	}
}

func TestParseGossipConnectionLine_Malformed(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(""),
		[]byte("not json"),
		[]byte("{}"),
		[]byte(`["only timestamp"]`),
		[]byte(`["ts", "not an array"]`),
		[]byte(`["ts", []]`),
	} {
		_, ok := parseGossipConnectionLine(line)
		if ok {
			t.Errorf("expected fail on %q", line)
		}
	}
}
