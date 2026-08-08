package monitors

import (
	"encoding/json"
	"fmt"
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

func TestRegisterStakeRowsRegistersLegacySignerAndStableAlias(t *testing.T) {
	const validator = "0x1111111111111111111111111111111111111111"
	const signer = "0x2222222222222222222222222222222222222222"
	metrics.ClearAddressCache()
	t.Cleanup(metrics.ClearAddressCache)

	count, mapping := registerStakeRows([][]interface{}{{validator, signer}})
	if count != 1 {
		t.Fatalf("mapping count = %d, want 1", count)
	}
	if got := mapping[signer]; got != validator {
		t.Fatalf("mapping[%q] = %q, want %q", signer, got, validator)
	}
	if _, exists := mapping["0x2222..2222"]; exists {
		t.Fatal("provisional mapping stored a lossy truncated signer key")
	}
	if got := metrics.ExpandAddress("0x2222..2222"); got != signer {
		t.Fatalf("registered signer expansion = %q, want %q", got, signer)
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

func TestDecodeStatusStakeRowsRejectsInvalidAndDuplicateIdentities(t *testing.T) {
	for _, raw := range []string{
		`[["not-an-address",1]]`,
		`[["0x1111111111111111111111111111111111111111",-1]]`,
		`[["0x1111111111111111111111111111111111111111",1],["0x1111111111111111111111111111111111111111",2]]`,
		`[["0x1111111111111111111111111111111111111111",1],["0x1111..1111",2]]`,
		`[["0x1111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222"],["0x3333333333333333333333333333333333333333","0x2222222222222222222222222222222222222222"]]`,
		`[["0x1111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222"],["0x3333333333333333333333333333333333333333","0x2222..2222"]]`,
		`[["0x1111111111111111111111111111111111111111",1],["0x3333333333333333333333333333333333333333","0x2222222222222222222222222222222222222222"]]`,
	} {
		if _, err := decodeStatusStakeRows(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid current_stakes generation accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		`[["0x1111..1111",1],["0x2222..2222",2]]`,
		`[["0x1111..1111","0x3333..3333"],["0x2222..2222","0x4444..4444"]]`,
	} {
		if _, err := decodeStatusStakeRows(json.RawMessage(raw)); err != nil {
			t.Fatalf("historical truncated identities rejected: %v", err)
		}
	}
}

func TestParseValidatorStatusLineRejectsNullRequiredFields(t *testing.T) {
	const timestamp = `"2026-07-04T08:00:00.000000000"`
	const home = `"0x2222222222222222222222222222222222222222"`
	validBody := `{"home_validator":` + home + `,"round":1,"current_stakes":[],"current_jailed_validators":[]}`
	for name, line := range map[string]string{
		"timestamp":            `[null,` + validBody + `]`,
		"body":                 `[` + timestamp + `,null]`,
		"home_validator":       `[` + timestamp + `,{"home_validator":null,"round":1,"current_stakes":[],"current_jailed_validators":[]}]`,
		"empty home_validator": `[` + timestamp + `,{"home_validator":"","round":1,"current_stakes":[],"current_jailed_validators":[]}]`,
		"round":                `[` + timestamp + `,{"home_validator":` + home + `,"round":null,"current_stakes":[],"current_jailed_validators":[]}]`,
		"current_stakes":       `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":null,"current_jailed_validators":[]}]`,
		"wrapped stakes":       `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":{"validator_to_stake":null},"current_jailed_validators":[]}]`,
		"jailed validators":    `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":[],"current_jailed_validators":null}]`,
		"null stake row":       `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":[null],"current_jailed_validators":[]}]`,
		"null stake identity":  `[` + timestamp + `,{"home_validator":` + home + `,"round":1,"current_stakes":[[null,1]],"current_jailed_validators":[]}]`,
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

func TestGetValidatorStatusCorrelatesFullAndTruncatedLegacySigner(t *testing.T) {
	const validator = "0x3333333333333333333333333333333333333333"
	const fullSigner = "0x4444444444444444444444444444444444444444"
	const truncatedSigner = "0x4444..4444"

	for name, pair := range map[string][2]string{
		"full home and truncated row": {fullSigner, truncatedSigner},
		"truncated home and full row": {truncatedSigner, fullSigner},
	} {
		t.Run(name, func(t *testing.T) {
			nodeHome := t.TempDir()
			statusDir := filepath.Join(nodeHome, "data", "node_logs", "status", "hourly", "20260808")
			if err := os.MkdirAll(statusDir, 0o755); err != nil {
				t.Fatal(err)
			}
			line := `["2026-08-08T01:00:00.000000000",{"home_validator":"` + pair[0] + `","round":1,"current_stakes":[["` + validator + `","` + pair[1] + `"]]}]` + "\n"
			if err := os.WriteFile(filepath.Join(statusDir, "1"), []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			got, isValidator := GetValidatorStatus(nodeHome)
			if !isValidator || got != validator {
				t.Fatalf("GetValidatorStatus = %q, %t; want %q, true", got, isValidator, validator)
			}
		})
	}
}

func TestGetValidatorStatusDoesNotGuessAmbiguousTruncatedLegacySigner(t *testing.T) {
	validatorOne := fmt.Sprintf("0x1111%032x1111", 1)
	validatorTwo := fmt.Sprintf("0x2222%032x2222", 2)
	signerOne := fmt.Sprintf("0xabcd%032x1234", 1)
	signerTwo := fmt.Sprintf("0xabcd%032x1234", 2)
	const truncatedSigner = "0xabcd..1234"

	metrics.ClearAddressCache()
	metrics.ReplaceValidatorSnapshot(nil)
	t.Cleanup(func() {
		metrics.ClearAddressCache()
		metrics.ReplaceValidatorSnapshot(nil)
	})

	nodeHome := t.TempDir()
	statusDir := filepath.Join(nodeHome, "data", "node_logs", "status", "hourly", "20260808")
	if err := os.MkdirAll(statusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(statusDir, "1")
	write := func(home string) {
		t.Helper()
		line := fmt.Sprintf(`["2026-08-08T01:00:00.000000000",{"home_validator":%q,"round":1,"current_stakes":[[%q,%q],[%q,%q]]}]`+"\n",
			home, validatorOne, signerOne, validatorTwo, signerTwo)
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(truncatedSigner)
	if got, isValidator := GetValidatorStatus(nodeHome); !isValidator || got != "" {
		t.Fatalf("ambiguous truncated home = %q, %t; want unknown validator, true role", got, isValidator)
	}
	write(signerTwo)
	if got, isValidator := GetValidatorStatus(nodeHome); !isValidator || got != validatorTwo {
		t.Fatalf("exact full home = %q, %t; want %q, true", got, isValidator, validatorTwo)
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

func TestReadLastLineIgnoresUnterminatedSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status")
	first := `["2026-08-08T00:00:00.000000000",{"round":1}]`
	second := `["2026-08-08T00:01:00.000000000",{"round":2}]`
	if err := os.WriteFile(path, []byte(first+"\n"+second), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLastLine(path); err != nil || got != first {
		t.Fatalf("last committed line = %q, %v; want first", got, err)
	}
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLastLine(path); err == nil || got != "" {
		t.Fatalf("unterminated-only file returned %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(first+"\n"+second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLastLine(path); err != nil || got != second {
		t.Fatalf("completed second line = %q, %v", got, err)
	}
}
