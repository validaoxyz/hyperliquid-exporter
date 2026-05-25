package monitors

import (
	"testing"
)

func TestParseGossipConnectionLine_KnownEvents(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"verified gossip rpc",
			`["2026-05-25T06:59:53.606639758",["verified gossip rpc",{"Ip":"162.19.103.75"}]]`,
			"verified_gossip_rpc",
		},
		{
			"performing checks on stream",
			`["2026-05-25T06:59:53.6",["performing checks on stream",{"foo":1}]]`,
			"performing_checks_on_stream",
		},
		{
			"error checking connection",
			`["2026-05-25T06:59:53.6",["error checking connection",{"err":"x"}]]`,
			"error_checking_connection",
		},
		{
			"rejecting gossip stream because max peers reached",
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
