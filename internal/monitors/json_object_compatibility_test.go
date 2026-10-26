package monitors

import (
	"encoding/json"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/testutil"
)

// Frozen from a9f5b37051504cb8e534f369172b5f76956c0c35.
func frozenRawJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func TestRawJSONObjectCompatibility(t *testing.T) {
	cases := testutil.JSONCompatibilityCases()
	accepted := 0
	for _, item := range cases {
		want := frozenRawJSONObject(item.Raw)
		if got := rawJSONObject(item.Raw); got != want {
			t.Fatalf("case %s: got %v, want %v, raw=%q", item.Name, got, want, item.Raw)
		}
		if want {
			accepted++
		}
	}
	t.Logf("cases=%d accepted=%d rejected=%d corpus_sha256=%s", len(cases), accepted, len(cases)-accepted, testutil.JSONCompatibilityFingerprint(cases))
}

func TestRawJSONObjectRejectsNonJSONWhitespace(t *testing.T) {
	for _, raw := range []string{"\v{}", "{}\f", "\u00a0{}", "{}\u2003", "\u3000{}\u3000"} {
		if rawJSONObject([]byte(raw)) {
			t.Fatalf("accepted non-JSON whitespace: %q", raw)
		}
	}
	if !rawJSONObject([]byte(" \t\r\n{} \t\r\n")) {
		t.Fatal("rejected JSON whitespace")
	}
}

func FuzzRawJSONObjectCompatibility(f *testing.F) {
	for _, raw := range []string{``, `{}`, `null`, `[]`, `{"k":null}`, `{"k":1,"k":2}`, `{"\ud800":1,"\ufffd":2}`, "\u00a0{}", string([]byte{'{', '"', 0xff, '"', ':', '1', '}'})} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if got, want := rawJSONObject(raw), frozenRawJSONObject(raw); got != want {
			t.Fatalf("got %v, want %v, raw=%q", got, want, raw)
		}
	})
}
