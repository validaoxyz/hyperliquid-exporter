package monitors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
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

func TestDecodeStatusStakeRowsRejectsNullAndMalformedRows(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`{"validator_to_stake":null}`,
		`[null]`,
		`[["0x1111111111111111111111111111111111111111"]]`,
		`[[null,1]]`,
		`[["0x1111111111111111111111111111111111111111",null]]`,
	} {
		if _, err := decodeStatusStakeRows(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid current_stakes accepted: %s", raw)
		}
	}

	for _, raw := range []string{
		`[]`,
		`{"validator_to_stake":[]}`,
		`[["0x1111111111111111111111111111111111111111",0]]`,
		`[["0x1111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222"]]`,
	} {
		if _, err := decodeStatusStakeRows(json.RawMessage(raw)); err != nil {
			t.Fatalf("valid current_stakes rejected: %s: %v", raw, err)
		}
	}
}

func TestDecodeStatusStakeRowsRejectsOversizedGeneration(t *testing.T) {
	const row = `["0x1111111111111111111111111111111111111111",1]`
	raw := `[` + strings.Repeat(row+`,`, validatorSummaryLimit) + row + `]`
	if _, err := decodeStatusStakeRows(json.RawMessage(raw)); err == nil {
		t.Fatalf("accepted %d current_stakes rows", validatorSummaryLimit+1)
	}
}

func TestParseValidatorStatusLineRejectsNullRequiredFields(t *testing.T) {
	const timestamp = `"2026-07-04T08:00:00.000000000"`
	const home = `"0x2222222222222222222222222222222222222222"`
	validBody := `{"home_validator":` + home + `,"round":1,"current_stakes":[],"current_jailed_validators":[]}`
	for name, line := range map[string]string{
		"timestamp":           `[null,` + validBody + `]`,
		"body":                `[` + timestamp + `,null]`,
		"home_validator":      `[` + timestamp + `,{"home_validator":null,"round":1,"current_stakes":[],"current_jailed_validators":[]}]`,
		"round":               `[` + timestamp + `,{"home_validator":` + home + `,"round":null,"current_stakes":[],"current_jailed_validators":[]}]`,
		"current_stakes":      `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":null,"current_jailed_validators":[]}]`,
		"wrapped stakes":      `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":{"validator_to_stake":null},"current_jailed_validators":[]}]`,
		"jailed validators":   `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":[],"current_jailed_validators":null}]`,
		"null stake row":      `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":[null],"current_jailed_validators":[]}]`,
		"null stake identity": `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":[[null,1]],"current_jailed_validators":[]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseValidatorStatusLine(line); err == nil {
				t.Fatalf("accepted null %s: %s", name, line)
			}
		})
	}

	if parsed, err := parseValidatorStatusLine(`[` + timestamp + `,{"home_validator":` + home + `,"round":0,"current_stakes":[]}]`); err != nil || parsed.Round != 0 || parsed.JailedFieldPresent {
		t.Fatalf("valid zero/omitted optional field rejected: %+v, %v", parsed, err)
	}
}

func TestNullJailedSetDoesNotReconcilePreviousGeneration(t *testing.T) {
	oldJailed := jailedLocalPrev
	const signer = "0x1111111111111111111111111111111111111111"
	labels := [3]string{"validator", signer, "name"}
	jailedLocalPrev = map[string][3]string{signer: labels}
	t.Cleanup(func() { jailedLocalPrev = oldJailed })

	line := `[` + `"2026-07-04T08:00:00.000000000"` + `,{"home_validator":"0x2222222222222222222222222222222222222222","round":1,"current_stakes":[],"current_jailed_validators":null}]`
	if err := processValidatorStatusLine(line); err == nil {
		t.Fatal("null jailed set was accepted")
	}
	if got, ok := jailedLocalPrev[signer]; !ok || got != labels || len(jailedLocalPrev) != 1 {
		t.Fatalf("invalid status reconciled previous jailed generation: %v", jailedLocalPrev)
	}
}

func TestProcessValidatorStatusLineNewSchema(t *testing.T) {
	oldJailed := jailedLocalPrev
	jailedLocalPrev = make(map[string][3]string)
	t.Cleanup(func() { jailedLocalPrev = oldJailed })
	line := `["2026-07-04T08:00:00.000000000",{"home_validator":"0x2222222222222222222222222222222222222222","round":770617293,` +
		`"current_stakes":{"validator_to_stake":[["0x1111111111111111111111111111111111111111",9],["0x3333333333333333333333333333333333333333",652]]},` +
		`"current_jailed_validators":["0x1111111111111111111111111111111111111111"],` +
		`"disconnected_validators":[],"heartbeat_statuses":[]}]`
	if err := processValidatorStatusLine(line); err != nil {
		t.Fatalf("new-schema status line failed to parse: %v", err)
	}
	if _, ok := jailedLocalPrev["0x1111111111111111111111111111111111111111"]; !ok {
		t.Fatal("jailed-local set was not published from new-schema line")
	}
	if !validatorCollectorHasLabels(metrics.HLConsensusValidatorJailedLocal, map[string]string{
		"validator": "unknown",
		"signer":    "0x1111111111111111111111111111111111111111",
		"name":      "unknown",
	}) {
		t.Fatal("node-local jailed signer was not published with explicit identity kind")
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
	oldJailed := jailedLocalPrev
	const priorSigner = "0x1111111111111111111111111111111111111111"
	priorLabels := [3]string{"validator", priorSigner, "name"}
	jailedLocalPrev = map[string][3]string{priorSigner: priorLabels}
	t.Cleanup(func() { jailedLocalPrev = oldJailed })

	line := `["2026-07-04T08:00:00.000000000",{"home_validator":"0x4444444444444444444444444444444444444444","round":1,` +
		`"current_stakes":[["0x3333333333333333333333333333333333333333","0x4444444444444444444444444444444444444444"]]}]`
	if err := processValidatorStatusLine(line); err != nil {
		t.Fatalf("legacy status line failed to parse: %v", err)
	}
	if got, ok := jailedLocalPrev[priorSigner]; !ok || got != priorLabels || len(jailedLocalPrev) != 1 {
		t.Fatalf("omitted legacy jailed field impersonated empty: %v", jailedLocalPrev)
	}
}

func TestReadValidatorStatusWithdrawsJailedRowsOnAbsenceAndStaleness(t *testing.T) {
	oldJailed := jailedLocalPrev
	oldAddress := lastValidatorAddress
	oldSource := lastMappingSource
	jailedLocalPrev = make(map[string][3]string)
	lastValidatorAddress = ""
	lastMappingSource = ""
	metrics.HLConsensusValidatorJailedLocal.Reset()
	t.Cleanup(func() {
		metrics.HLConsensusValidatorJailedLocal.Reset()
		jailedLocalPrev = oldJailed
		lastValidatorAddress = oldAddress
		lastMappingSource = oldSource
	})

	nodeHome := t.TempDir()
	statusDir := filepath.Join(nodeHome, "data", "node_logs", "status", "hourly")
	dateDir := filepath.Join(statusDir, "20260808")
	path := filepath.Join(dateDir, "1")
	line := `["2026-08-08T01:00:00.000000000",{"home_validator":"0x2222222222222222222222222222222222222222","round":1,"current_stakes":[],"current_jailed_validators":["0x1111111111111111111111111111111111111111"]}]` + "\n"
	write := func() {
		t.Helper()
		if err := os.MkdirAll(dateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	assertPublished := func() {
		t.Helper()
		if rows := b03CollectorRows(t, metrics.HLConsensusValidatorJailedLocal); len(rows) != 1 {
			t.Fatalf("jailed-local rows = %d, want 1", len(rows))
		}
	}
	assertWithdrawn := func() {
		t.Helper()
		if rows := b03CollectorRows(t, metrics.HLConsensusValidatorJailedLocal); len(rows) != 0 {
			t.Fatalf("withdrawal retained %d jailed-local rows", len(rows))
		}
		if len(jailedLocalPrev) != 0 {
			t.Fatalf("withdrawal retained jailed-local state: %v", jailedLocalPrev)
		}
	}

	write()
	if err := readValidatorStatus(nodeHome); err != nil {
		t.Fatal(err)
	}
	assertPublished()
	if err := os.RemoveAll(statusDir); err != nil {
		t.Fatal(err)
	}
	if err := readValidatorStatus(nodeHome); err != nil {
		t.Fatal(err)
	}
	assertWithdrawn()

	write()
	if err := readValidatorStatus(nodeHome); err != nil {
		t.Fatal(err)
	}
	assertPublished()
	stale := time.Now().Add(-13 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := readValidatorStatus(nodeHome); err != nil {
		t.Fatal(err)
	}
	assertWithdrawn()
}
