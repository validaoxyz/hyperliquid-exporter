package replica

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/testutil"
)

// Frozen from a9f5b37051504cb8e534f369172b5f76956c0c35.
func frozenNormalizeReplicaOutcome(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var scalar string
	if err := json.Unmarshal(raw, &scalar); err == nil {
		if scalar == "" {
			return "", false
		}
		if scalar == "success" {
			return "success", true
		}
		return "other", true
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return "", false
	}
	for key := range object {
		switch key {
		case "resting", "error":
			return key, true
		case "waitingForTrigger":
			return "waiting_for_trigger", true
		default:
			return "other", true
		}
	}
	return "", false
}

func TestNormalizeReplicaOutcomeCompatibility(t *testing.T) {
	cases := testutil.JSONCompatibilityCases()
	accepted := 0
	for _, item := range cases {
		want, wantOK := frozenNormalizeReplicaOutcome(item.Raw)
		got, gotOK := normalizeReplicaOutcome(item.Raw)
		if got != want || gotOK != wantOK {
			t.Fatalf("case %s: got %q/%v, want %q/%v, raw=%q", item.Name, got, gotOK, want, wantOK, item.Raw)
		}
		if wantOK {
			accepted++
		}
	}
	t.Logf("cases=%d accepted=%d rejected=%d corpus_sha256=%s", len(cases), accepted, len(cases)-accepted, testutil.JSONCompatibilityFingerprint(cases))
}

func TestNormalizeReplicaOutcomeWhitespaceAndDuplicateKeys(t *testing.T) {
	cases := []struct {
		raw     string
		outcome string
		ok      bool
	}{
		{`{"resting":1,"resting":null}`, "resting", true},
		{`{"resting":1,"\u0072esting":null}`, "resting", true},
		{`{"resting":1,"error":null}`, "", false},
		{`{"\ud800":1,"\ufffd":2}`, "other", true},
		{string([]byte{'{', '"', 0xff, '"', ':', '1', ',', '"', 0xfe, '"', ':', '2', '}'}), "other", true},
		{`"su\u0063cess"`, "success", true},
		{`"\ud800"`, "other", true},
		{" \t\r\n\"success\" \t\r\n", "success", true},
		{"\u00a0\"success\"", "", false},
		{"\"success\"\v", "", false},
		{"\u2003{\"resting\":{}}", "", false},
	}
	for _, item := range cases {
		if got, ok := normalizeReplicaOutcome([]byte(item.raw)); got != item.outcome || ok != item.ok {
			t.Fatalf("raw=%q: got %q/%v, want %q/%v", item.raw, got, ok, item.outcome, item.ok)
		}
	}
}

func FuzzNormalizeReplicaOutcomeCompatibility(f *testing.F) {
	for _, raw := range []string{``, `null`, `"success"`, `"future"`, `""`, `{"resting":{}}`, `{"error":"constructed"}`, `{"resting":1,"\u0072esting":null}`, `{"\ud800":1,"\ufffd":2}`, "\u00a0\"success\"", string([]byte{'"', 0xff, '"'})} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		want, wantOK := frozenNormalizeReplicaOutcome(raw)
		got, gotOK := normalizeReplicaOutcome(raw)
		if got != want || gotOK != wantOK {
			t.Fatalf("got %q/%v, want %q/%v, raw=%q", got, gotOK, want, wantOK, raw)
		}
		if gotOK && got != "success" && got != "resting" && got != "error" && got != "waiting_for_trigger" && got != "other" {
			t.Fatalf("unbounded outcome label: %q", got)
		}
	})
}
