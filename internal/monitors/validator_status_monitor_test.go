package monitors

import (
	"encoding/json"
	"testing"
)

func TestStatusStakeRowsLegacyArray(t *testing.T) {
	raw := json.RawMessage(`[["0x1111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222"],["0x3333333333333333333333333333333333333333","0x4444444444444444444444444444444444444444"]]`)
	rows := statusStakeRows(raw)
	if len(rows) != 2 {
		t.Fatalf("legacy rows = %d, want 2", len(rows))
	}
	if v, _ := rows[0][0].(string); v != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("row[0][0] = %v", rows[0][0])
	}
	if s, ok := rows[0][1].(string); !ok || s != "0x2222222222222222222222222222222222222222" {
		t.Fatalf("legacy row[1] should be a signer string, got %v", rows[0][1])
	}
}

func TestStatusStakeRowsValidatorToStake(t *testing.T) {
	raw := json.RawMessage(`{"validator_to_stake":[["0x1111111111111111111111111111111111111111",9],["0x3333333333333333333333333333333333333333",652]]}`)
	rows := statusStakeRows(raw)
	if len(rows) != 2 {
		t.Fatalf("wrapped rows = %d, want 2", len(rows))
	}
	if _, ok := rows[0][1].(string); ok {
		t.Fatal("stake row[1] must not be a string")
	}
	count, mapping := registerStakeRows(rows)
	if count != 0 {
		t.Fatalf("stake-shaped rows produced %d signer mappings, want 0", count)
	}
	if len(mapping) != 0 {
		t.Fatalf("stake-shaped rows produced local mapping %v, want empty", mapping)
	}
}

func TestStatusStakeRowsGarbage(t *testing.T) {
	for _, raw := range []string{``, `null`, `42`, `"nope"`, `{"unrelated":1}`} {
		if rows := statusStakeRows(json.RawMessage(raw)); len(rows) != 0 {
			t.Fatalf("raw %q produced rows %v, want none", raw, rows)
		}
	}
}

func TestProcessValidatorStatusLineNewSchema(t *testing.T) {
	line := `["2026-07-04T08:00:00.000000000",{"home_validator":"0x2222222222222222222222222222222222222222","round":770617293,` +
		`"current_stakes":{"validator_to_stake":[["0x1111111111111111111111111111111111111111",9],["0x3333333333333333333333333333333333333333",652]]},` +
		`"current_jailed_validators":["0x1111111111111111111111111111111111111111"],` +
		`"disconnected_validators":[],"heartbeat_statuses":[]}]`
	if err := processValidatorStatusLine(line); err != nil {
		t.Fatalf("new-schema status line failed to parse: %v", err)
	}
	if !jailedLocalPrev["0x1111111111111111111111111111111111111111"] {
		t.Fatal("jailed-local set was not published from new-schema line")
	}
	// unjail everyone: set must clear
	line2 := `["2026-07-04T08:02:00.000000000",{"home_validator":"0x2222222222222222222222222222222222222222","round":770617400,` +
		`"current_stakes":{"validator_to_stake":[]},"current_jailed_validators":[]}]`
	if err := processValidatorStatusLine(line2); err != nil {
		t.Fatalf("empty-jailed status line failed: %v", err)
	}
	if len(jailedLocalPrev) != 0 {
		t.Fatalf("jailed-local set not cleared: %v", jailedLocalPrev)
	}
}

func TestProcessValidatorStatusLineLegacySchema(t *testing.T) {
	line := `["2026-07-04T08:00:00.000000000",{"home_validator":"0x4444444444444444444444444444444444444444","round":1,` +
		`"current_stakes":[["0x3333333333333333333333333333333333333333","0x4444444444444444444444444444444444444444"]]}]`
	if err := processValidatorStatusLine(line); err != nil {
		t.Fatalf("legacy status line failed to parse: %v", err)
	}
}
