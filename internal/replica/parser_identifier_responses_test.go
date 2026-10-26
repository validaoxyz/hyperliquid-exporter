package replica

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestExtractMetricsClassifiesConstructedIdentifierResponses(t *testing.T) {
	body, err := os.ReadFile("testdata/testnet_identifier_responses.constructed.json")
	if err != nil {
		t.Fatal(err)
	}
	p, block := parseReplicaFixture(t, string(body))
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalActions != 4 || got.TotalOperations != 4 || got.TotalBundles != 1 {
		t.Fatalf("unexpected request totals: %+v", got)
	}
	wantActions := map[string]int{"spotDeploy": 2, "govPropose": 1, "govVote": 1}
	if !reflect.DeepEqual(got.ActionCounts, wantActions) || !reflect.DeepEqual(got.OperationCounts, wantActions) || len(got.ParserEvents) != 0 {
		t.Fatalf("request classification = actions:%v operations:%v events:%v", got.ActionCounts, got.OperationCounts, got.ParserEvents)
	}
	responses := got.Responses
	if responses.Coverage != "available" || responses.CountRelation != "equal" || responses.Records != 4 || responses.MalformedRecords != 0 || responses.MalformedContainers != 0 || !reflect.DeepEqual(responses.ActionStatuses, map[string]int{"ok": 4}) || len(responses.Outcomes) != 0 {
		t.Fatalf("identifier responses must count only envelope statuses: %+v", responses)
	}
}

func TestParseReplicaNumericIdentifiers(t *testing.T) {
	for _, responseType := range []string{"token", "spot"} {
		for name, response := range map[string]string{
			"zero":                    `{"type":"` + responseType + `","data":0}`,
			"positive":                `{"type":"` + responseType + `","data":17}`,
			"uint64 maximum":          `{"type":"` + responseType + `","data":18446744073709551615}`,
			"field order":             `{"data":17,"type":"` + responseType + `"}`,
			"case insensitive keys":   `{"TYPE":"` + responseType + `","DATA":17}`,
			"repeated integer":        `{"type":"` + responseType + `","data":3,"data":17}`,
			"null before integer":     `{"type":"` + responseType + `","data":null,"data":17}`,
			"last type wins":          `{"type":"future","type":"` + responseType + `","data":17}`,
			"null preserves type":     `{"type":"` + responseType + `","type":null,"data":17}`,
			"unrelated field ignored": `{"type":"` + responseType + `","data":17,"detail":"constructed"}`,
		} {
			t.Run(responseType+"/"+name, func(t *testing.T) {
				assertIdentifierEnvelope(t, `{"status":"ok","response":`+response+`}`, true)
			})
		}
		for name, data := range map[string]string{
			"negative":        `-1`,
			"negative zero":   `-0`,
			"fraction":        `1.5`,
			"decimal integer": `1.0`,
			"exponent":        `1e2`,
			"overflow":        `18446744073709551616`,
			"string":          `"17"`,
			"boolean":         `true`,
			"empty array":     `[]`,
			"number array":    `[17]`,
			"null statuses":   `{"statuses":null}`,
			"invalid status":  `{"statuses":["success",null]}`,
		} {
			t.Run(responseType+"/reject/"+name, func(t *testing.T) {
				assertIdentifierEnvelope(t, `{"status":"ok","response":{"type":"`+responseType+`","data":`+data+`}}`, false)
			})
		}
	}
}

func TestParseReplicaGovernanceProposalHash(t *testing.T) {
	hash := `"0x` + strings.Repeat("ab", 32) + `"`
	for name, response := range map[string]string{
		"lower hex":               `{"type":"governanceProposal","data":` + hash + `}`,
		"upper hex":               `{"type":"governanceProposal","data":"0x` + strings.Repeat("AB", 32) + `"}`,
		"zero hash shape":         `{"type":"governanceProposal","data":"0x` + strings.Repeat("00", 32) + `"}`,
		"field order":             `{"data":` + hash + `,"type":"governanceProposal"}`,
		"case insensitive keys":   `{"TYPE":"governanceProposal","DATA":` + hash + `}`,
		"repeated hash":           `{"type":"governanceProposal","data":` + hash + `,"data":` + hash + `}`,
		"null before hash":        `{"type":"governanceProposal","data":null,"data":` + hash + `}`,
		"last string wins":        `{"type":"governanceProposal","data":"constructed","data":` + hash + `}`,
		"last type wins":          `{"type":"future","type":"governanceProposal","data":` + hash + `}`,
		"null preserves type":     `{"type":"governanceProposal","type":null,"data":` + hash + `}`,
		"unrelated field ignored": `{"type":"governanceProposal","data":` + hash + `,"detail":"constructed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertIdentifierEnvelope(t, `{"status":"ok","response":`+response+`}`, true)
		})
	}
	for name, data := range map[string]string{
		"empty string":       `""`,
		"opaque string":      `"constructed"`,
		"missing prefix":     `"` + strings.Repeat("ab", 32) + `"`,
		"upper prefix":       `"0X` + strings.Repeat("ab", 32) + `"`,
		"address width":      `"0x` + strings.Repeat("ab", 20) + `"`,
		"short hash":         `"0x` + strings.Repeat("a", 63) + `"`,
		"long hash":          `"0x` + strings.Repeat("a", 65) + `"`,
		"nonhex hash":        `"0x` + strings.Repeat("g", 64) + `"`,
		"unicode hash":       `"0x` + strings.Repeat("a", 62) + `é"`,
		"leading whitespace": `" 0x` + strings.Repeat("a", 64) + `"`,
		"trailing newline":   `"0x` + strings.Repeat("a", 64) + `\n"`,
		"integer":            `17`,
		"boolean":            `true`,
		"empty array":        `[]`,
		"hash array":         `[` + hash + `]`,
		"null statuses":      `{"statuses":null}`,
		"invalid status":     `{"statuses":["success",null]}`,
	} {
		t.Run("reject/"+name, func(t *testing.T) {
			assertIdentifierEnvelope(t, `{"status":"ok","response":{"type":"governanceProposal","data":`+data+`}}`, false)
		})
	}
}

func TestParseReplicaIdentifiersRequireExactSuccessfulEnvelope(t *testing.T) {
	for responseType, data := range map[string]string{
		"token":              `17`,
		"spot":               `17`,
		"governanceProposal": `"0x` + strings.Repeat("ab", 32) + `"`,
	} {
		for name, result := range map[string]string{
			"different type":          `{"status":"ok","response":{"type":"future","data":` + data + `}}`,
			"wrong case":              `{"status":"ok","response":{"type":"` + strings.ToUpper(responseType) + `","data":` + data + `}}`,
			"missing type":            `{"status":"ok","response":{"data":` + data + `}}`,
			"null type":               `{"status":"ok","response":{"type":null,"data":` + data + `}}`,
			"nonstring type":          `{"status":"ok","response":{"type":1,"data":` + data + `}}`,
			"error status":            `{"status":"err","response":{"type":"` + responseType + `","data":` + data + `}}`,
			"other status":            `{"status":"future","response":{"type":"` + responseType + `","data":` + data + `}}`,
			"missing status":          `{"response":{"type":"` + responseType + `","data":` + data + `}}`,
			"null status":             `{"status":null,"response":{"type":"` + responseType + `","data":` + data + `}}`,
			"value then null":         `{"status":"ok","response":{"type":"` + responseType + `","data":` + data + `,"data":null}}`,
			"object then value":       `{"status":"ok","response":{"type":"` + responseType + `","data":{},"data":` + data + `}}`,
			"value then object":       `{"status":"ok","response":{"type":"` + responseType + `","data":` + data + `,"data":{}}}`,
			"boolean then value":      `{"status":"ok","response":{"type":"` + responseType + `","data":false,"data":` + data + `}}`,
			"value then boolean":      `{"status":"ok","response":{"type":"` + responseType + `","data":` + data + `,"data":false}}`,
			"invalid type then valid": `{"status":"ok","response":{"type":1,"type":"` + responseType + `","data":` + data + `}}`,
			"valid type then invalid": `{"status":"ok","response":{"type":"` + responseType + `","type":1,"data":` + data + `}}`,
			"last type is different":  `{"status":"ok","response":{"type":"` + responseType + `","type":"future","data":` + data + `}}`,
		} {
			t.Run(responseType+"/"+name, func(t *testing.T) {
				assertIdentifierEnvelope(t, result, false)
			})
		}
	}
}

func TestParseReplicaIdentifiersPreserveGenericObjectPath(t *testing.T) {
	for _, responseType := range []string{"token", "spot", "governanceProposal"} {
		for name, field := range map[string]string{
			"missing data":   ``,
			"null data":      `,"data":null`,
			"object data":    `,"data":{}`,
			"empty statuses": `,"data":{"statuses":[]}`,
		} {
			for _, status := range []string{"ok", "err", "future"} {
				t.Run(responseType+"/"+name+"/"+status, func(t *testing.T) {
					raw := json.RawMessage(`{"Full":[["constructed",[{"res":{"status":"` + status + `","response":{"type":"` + responseType + `"` + field + `}}}]]]}`)
					got := parseReplicaResponses(raw, 1)
					label := status
					if label == "future" {
						label = "other"
					}
					if got.Coverage != "available" || got.Records != 1 || got.CountRelation != "equal" || got.MalformedRecords != 0 || got.MalformedContainers != 0 || !reflect.DeepEqual(got.ActionStatuses, map[string]int{label: 1}) || len(got.Outcomes) != 0 {
						t.Fatalf("generic object path changed: %+v", got)
					}
				})
			}
		}
	}
}

func assertIdentifierEnvelope(t *testing.T, result string, accepted bool) {
	t.Helper()
	raw := json.RawMessage(`{"Full":[["constructed",[{"res":` + result + `}]]]}`)
	got := parseReplicaResponses(raw, 1)
	if got.Records != 1 || got.CountRelation != "equal" || got.MalformedContainers != 0 || len(got.Outcomes) != 0 {
		t.Fatalf("identifier response changed count relation or published outcomes: %+v", got)
	}
	if accepted {
		if got.Coverage != "available" || got.MalformedRecords != 0 || !reflect.DeepEqual(got.ActionStatuses, map[string]int{"ok": 1}) {
			t.Fatalf("valid identifier must count one envelope status: %+v", got)
		}
	} else if got.Coverage != "malformed" || got.MalformedRecords != 1 || len(got.ActionStatuses) != 0 {
		t.Fatalf("invalid identifier must not publish partial status: %+v", got)
	}
}
