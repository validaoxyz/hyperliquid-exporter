package monitors

import (
	"testing"
)

func TestParseTCPTrafficLine_RealSample(t *testing.T) {
	// Real sample line lifted from a live mainnet peer's
	// data/tcp_traffic/hourly/<date>/<hour>. Three flows: one In, two Out.
	line := []byte(`["2026-05-25T10:37:08.672768629",[[["In","52.198.167.169",4001],0.8248638739917559],[["Out","162.19.103.75",4001],0.8252117484278043],[["Out","79.137.101.77",4001],0.8252116450033067]]]`)

	ts, in, out, ok := parseTCPTrafficLine(line)
	if !ok {
		t.Fatalf("parse returned ok=false on valid sample")
	}
	if ts.IsZero() {
		t.Errorf("expected non-zero timestamp")
	}
	if len(in) != 1 {
		t.Fatalf("expected 1 inbound flow, got %d", len(in))
	}
	if in[0].ip != "52.198.167.169" || in[0].value != 0.8248638739917559 {
		t.Errorf("inbound mismatch: %+v", in[0])
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 outbound flows, got %d", len(out))
	}
	// Outbound IPs are inserted via a map walk so order is non-deterministic;
	// assert presence by sum.
	outIPs := map[string]float64{}
	for _, p := range out {
		outIPs[p.ip] = p.value
	}
	if outIPs["162.19.103.75"] == 0 || outIPs["79.137.101.77"] == 0 {
		t.Errorf("missing expected outbound IP in %+v", out)
	}
}

func TestParseTCPTrafficLine_Malformed(t *testing.T) {
	cases := [][]byte{
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{}`),
		[]byte(`[1,2,3]`),                            // wrong outer arity
		[]byte(`["2026-01-01T00:00:00", "garbage"]`), // inner not array
	}
	for i, line := range cases {
		_, _, _, ok := parseTCPTrafficLine(line)
		if ok {
			t.Errorf("case %d: expected ok=false on malformed input %q", i, line)
		}
	}
}

func TestParseTCPTrafficLine_DedupesSameIPDifferentPorts(t *testing.T) {
	// One peer IP, two ports (4001 + 4002). Must collapse into a
	// single inbound row with summed bytes, otherwise parent_peer ends
	// up logging "ambiguous parent peer: top=X runner-up=X".
	line := []byte(`["2026-05-25T10:00:00",[[["In","1.1.1.1",4001],0.4],[["In","1.1.1.1",4002],0.6]]]`)
	_, in, _, ok := parseTCPTrafficLine(line)
	if !ok {
		t.Fatalf("parse failed")
	}
	if len(in) != 1 {
		t.Fatalf("expected 1 deduped inbound row, got %d (%+v)", len(in), in)
	}
	const want = 1.0
	if got := in[0].value; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("expected summed value %v, got %v", want, got)
	}
	if in[0].ip != "1.1.1.1" {
		t.Errorf("ip = %q", in[0].ip)
	}
}

func TestParseTCPTrafficLine_SkipsBadFlows(t *testing.T) {
	// Two valid flows and one with a corrupted inner key shape; expect the
	// good flows to land and the bad one to be silently skipped.
	line := []byte(`["2026-05-25T10:00:00",[[["In","1.1.1.1",4001],0.5],"junk",[["Out","2.2.2.2",4001],1.5]]]`)
	_, in, out, ok := parseTCPTrafficLine(line)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if len(in) != 1 || in[0].ip != "1.1.1.1" {
		t.Errorf("in: %+v", in)
	}
	if len(out) != 1 || out[0].ip != "2.2.2.2" {
		t.Errorf("out: %+v", out)
	}
}

func TestLastFullLine(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantOK  bool
	}{
		{"trailing newline", "a\nb\nc\n", "c", true},
		{"no trailing newline", "a\nb\nc", "c", true},
		{"single line no newline", "alone", "alone", true},
		{"empty", "", "", false},
		{"only newlines", "\n\n\n", "", false},
		{"torn last", "good\nbad", "bad", true},
		{"trailing whitespace", "x\n   \n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := lastFullLine([]byte(c.input))
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (input %q)", ok, c.wantOK, c.input)
			}
			if string(got) != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}
