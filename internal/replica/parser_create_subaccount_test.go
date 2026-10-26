package replica

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseReplicaCreateSubAccountAddress(t *testing.T) {
	address := `"0x` + strings.Repeat("ab", 20) + `"`
	for name, response := range map[string]string{
		"lower hex":               `{"type":"createSubAccount","data":` + address + `}`,
		"upper hex":               `{"type":"createSubAccount","data":"0x` + strings.Repeat("AB", 20) + `"}`,
		"mixed hex":               `{"type":"createSubAccount","data":"0x` + strings.Repeat("aB", 20) + `"}`,
		"zero address shape":      `{"type":"createSubAccount","data":"0x` + strings.Repeat("00", 20) + `"}`,
		"field order":             `{"data":` + address + `,"type":"createSubAccount"}`,
		"case insensitive keys":   `{"TYPE":"createSubAccount","DATA":` + address + `}`,
		"repeated address":        `{"type":"createSubAccount","data":` + address + `,"DATA":` + address + `}`,
		"null before address":     `{"type":"createSubAccount","data":null,"data":` + address + `}`,
		"last string wins":        `{"type":"createSubAccount","data":"opaque","data":` + address + `}`,
		"last type wins":          `{"type":"future","type":"createSubAccount","data":` + address + `}`,
		"null preserves type":     `{"type":"createSubAccount","type":null,"data":` + address + `}`,
		"unrelated field ignored": `{"type":"createSubAccount","data":` + address + `,"detail":"opaque"}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`{"Full":[["constructed",[{"res":{"status":"ok","response":` + response + `}}]]]}`)
			got := parseReplicaResponses(raw, 1)
			if got.Coverage != "available" || got.Records != 1 || got.CountRelation != "equal" || got.MalformedRecords != 0 || got.MalformedContainers != 0 || !reflect.DeepEqual(got.ActionStatuses, map[string]int{"ok": 1}) || len(got.Outcomes) != 0 {
				t.Fatalf("createSubAccount address must count only its envelope status: %+v", got)
			}
		})
	}
}

func TestParseReplicaCreateSubAccountRejectsUnsupportedData(t *testing.T) {
	for name, data := range map[string]string{
		"empty string":       `""`,
		"opaque string":      `"opaque"`,
		"missing prefix":     `"` + strings.Repeat("ab", 20) + `"`,
		"upper prefix":       `"0X` + strings.Repeat("ab", 20) + `"`,
		"short address":      `"0x` + strings.Repeat("a", 39) + `"`,
		"long address":       `"0x` + strings.Repeat("a", 41) + `"`,
		"nonhex address":     `"0x` + strings.Repeat("g", 40) + `"`,
		"unicode address":    `"0x` + strings.Repeat("a", 38) + `é"`,
		"leading whitespace": `" 0x` + strings.Repeat("a", 40) + `"`,
		"trailing newline":   `"0x` + strings.Repeat("a", 40) + `\n"`,
		"number":             `42`,
		"boolean":            `true`,
		"empty array":        `[]`,
		"address array":      `["0x` + strings.Repeat("ab", 20) + `"]`,
		"null statuses":      `{"statuses":null}`,
		"invalid statuses":   `{"statuses":["success",null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`{"Full":[["constructed",[{"res":{"status":"ok","response":{"type":"createSubAccount","data":` + data + `}}}]]]}`)
			got := parseReplicaResponses(raw, 1)
			if got.Coverage != "malformed" || got.Records != 1 || got.CountRelation != "equal" || got.MalformedRecords != 1 || got.MalformedContainers != 0 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 {
				t.Fatalf("unsupported data must not publish partial status or outcomes: %+v", got)
			}
		})
	}
}

func TestParseReplicaCreateSubAccountRequiresExactSuccessfulEnvelope(t *testing.T) {
	address := `"0x` + strings.Repeat("ab", 20) + `"`
	for name, result := range map[string]string{
		"different type":          `{"status":"ok","response":{"type":"future","data":` + address + `}}`,
		"validatorVote type":      `{"status":"ok","response":{"type":"validatorVote","data":` + address + `}}`,
		"setGlobal type":          `{"status":"ok","response":{"type":"setGlobal","data":` + address + `}}`,
		"wrong case":              `{"status":"ok","response":{"type":"CreateSubAccount","data":` + address + `}}`,
		"missing type":            `{"status":"ok","response":{"data":` + address + `}}`,
		"null type":               `{"status":"ok","response":{"type":null,"data":` + address + `}}`,
		"nonstring type":          `{"status":"ok","response":{"type":1,"data":` + address + `}}`,
		"error status":            `{"status":"err","response":{"type":"createSubAccount","data":` + address + `}}`,
		"other status":            `{"status":"future","response":{"type":"createSubAccount","data":` + address + `}}`,
		"missing status":          `{"response":{"type":"createSubAccount","data":` + address + `}}`,
		"null status":             `{"status":null,"response":{"type":"createSubAccount","data":` + address + `}}`,
		"address then null":       `{"status":"ok","response":{"type":"createSubAccount","data":` + address + `,"data":null}}`,
		"address then opaque":     `{"status":"ok","response":{"type":"createSubAccount","data":` + address + `,"data":"opaque"}}`,
		"object then address":     `{"status":"ok","response":{"type":"createSubAccount","data":{},"data":` + address + `}}`,
		"address then object":     `{"status":"ok","response":{"type":"createSubAccount","data":` + address + `,"data":{}}}`,
		"boolean then address":    `{"status":"ok","response":{"type":"createSubAccount","data":false,"data":` + address + `}}`,
		"address then boolean":    `{"status":"ok","response":{"type":"createSubAccount","data":` + address + `,"data":false}}`,
		"invalid type then valid": `{"status":"ok","response":{"type":1,"type":"createSubAccount","data":` + address + `}}`,
		"valid type then invalid": `{"status":"ok","response":{"type":"createSubAccount","type":1,"data":` + address + `}}`,
		"last type is different":  `{"status":"ok","response":{"type":"createSubAccount","type":"future","data":` + address + `}}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`{"Full":[["constructed",[{"res":` + result + `}]]]}`)
			got := parseReplicaResponses(raw, 1)
			if got.Coverage != "malformed" || got.Records != 1 || got.CountRelation != "equal" || got.MalformedRecords != 1 || got.MalformedContainers != 0 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 {
				t.Fatalf("unsupported envelope must not publish partial status or outcomes: %+v", got)
			}
		})
	}
}

func TestParseReplicaCreateSubAccountPreservesGenericObjectPath(t *testing.T) {
	for name, response := range map[string]string{
		"missing data":   `{"type":"createSubAccount"}`,
		"null data":      `{"type":"createSubAccount","data":null}`,
		"object data":    `{"type":"createSubAccount","data":{}}`,
		"empty statuses": `{"type":"createSubAccount","data":{"statuses":[]}}`,
	} {
		for _, status := range []string{"ok", "err", "future"} {
			t.Run(name+"/"+status, func(t *testing.T) {
				raw := json.RawMessage(`{"Full":[["constructed",[{"res":{"status":"` + status + `","response":` + response + `}}]]]}`)
				got := parseReplicaResponses(raw, 1)
				label := status
				if label == "future" {
					label = "other"
				}
				if got.Coverage != "available" || got.Records != 1 || got.CountRelation != "equal" || got.MalformedRecords != 0 || got.MalformedContainers != 0 || !reflect.DeepEqual(got.ActionStatuses, map[string]int{label: 1}) || len(got.Outcomes) != 0 {
					t.Fatalf("createSubAccount must preserve the generic object path: %+v", got)
				}
			})
		}
	}
}
