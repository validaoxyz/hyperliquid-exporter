package monitors

import (
	"encoding/json"
	"testing"
)

// Real /tmp/crit_msg_latest_stats/hl-node.json sample taken from a
// live mainnet peer. Trimmed to two locations for brevity.
const critLocationsSample = `{
  "start_time": "2026-05-25T11:12:20.656667039",
  "n_bugs": 0,
  "n_crits": 24,
  "code_location_and_stats": [
    [
      {"fln": "/home/ubuntu/hl/code_Mainnet/base/src/gossip_rpc_client.rs", "line": 59},
      {"n": 7, "is_ignored": false, "first_seen": "2026-05-25T11:16:54.884388206", "last_seen": "2026-05-25T11:27:23.146756922", "first_msg": "...unexpected rpc response..."}
    ],
    [
      {"fln": "/home/ubuntu/hl/code_Mainnet/base/src/nv_stream.rs", "line": 198},
      {"n": 3, "is_ignored": false, "first_seen": "2026-05-25T11:17:00", "last_seen": "2026-05-25T11:28:00", "first_msg": "...reconnecting..."}
    ]
  ]
}`

func TestParseCritLocationsFile(t *testing.T) {
	var rich critMsgRichFile
	if err := json.Unmarshal([]byte(critLocationsSample), &rich); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if rich.NCrits != 24 {
		t.Errorf("n_crits = %d, want 24", rich.NCrits)
	}
	if len(rich.Stats) != 2 {
		t.Fatalf("got %d locations, want 2", len(rich.Stats))
	}

	// First location: key + detail.
	var key critLocation
	if err := json.Unmarshal(rich.Stats[0][0], &key); err != nil {
		t.Fatalf("key parse: %v", err)
	}
	if key.File == "" {
		t.Errorf("missing fln")
	}
	if key.Line != 59 {
		t.Errorf("line = %d, want 59", key.Line)
	}

	var detail critLocation
	if err := json.Unmarshal(rich.Stats[0][1], &detail); err != nil {
		t.Fatalf("detail parse: %v", err)
	}
	if detail.N != 7 {
		t.Errorf("n = %d, want 7", detail.N)
	}
	if detail.LastSeen == "" {
		t.Errorf("missing last_seen")
	}
}

func TestParseCritLocationsFile_Empty(t *testing.T) {
	var rich critMsgRichFile
	if err := json.Unmarshal([]byte(`{"start_time":"x","n_bugs":0,"n_crits":0,"code_location_and_stats":[]}`), &rich); err != nil {
		t.Fatal(err)
	}
	if len(rich.Stats) != 0 {
		t.Errorf("want 0 stats on empty doc, got %d", len(rich.Stats))
	}
}
