package replica

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCountJSONArrayElements(t *testing.T) {
	order := `{"a":0,"b":"BTC","p":"50000","s":"0.1","r":false,"t":{"limit":{"tif":"Gtc"}}}`
	cases := []struct {
		name string
		data string
		want int
	}{
		{"empty array", `[]`, 0},
		{"one order object", `[` + order + `]`, 1},
		{"two order objects", `[` + order + `,` + order + `]`, 2},
		{"one cancel", `[{"a":1,"o":12345}]`, 1},
		{"nested arrays inside objects", `[{"x":[1,2,3]},{"y":[[4,5]]}]`, 2},
		{"strings with commas and braces", `[{"s":"a,b}{c"},{"s":"d"}]`, 2},
	}
	for _, c := range cases {
		if got := countJSONArrayElements(json.RawMessage(c.data)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func parseReplicaFixture(t *testing.T, body string) (*Parser, *ReplicaBlock) {
	t.Helper()
	p := NewParser(1)
	block, err := p.ParseBlockFromLine([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return p, block
}

func TestExtractMetricsUsesHeightAndAcceptsValidEmptyBlock(t *testing.T) {
	p, block := parseReplicaFixture(t, `{
      "height":644381602,
      "abci_block":{
        "time":"2026-08-08T03:16:34.462864575",
        "round":818190913,
        "parent_round":818190912,
        "proposer":"0xproposer",
        "hardfork":{"version":1476,"round":817172405},
        "signed_action_bundles":[]
      }
    }`)
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 644381602 || got.Round != 818190913 {
		t.Fatalf("height/round = %d/%d", got.Height, got.Round)
	}
	if got.TotalBundles != 0 || got.TotalActions != 0 || got.TotalOperations != 0 {
		t.Fatalf("empty block totals = bundles:%d actions:%d operations:%d", got.TotalBundles, got.TotalActions, got.TotalOperations)
	}
	if got.Responses.Coverage != "unavailable" || got.Responses.CountRelation != "equal" {
		t.Fatalf("empty response coverage = %+v", got.Responses)
	}
}

func TestExtractMetricsRejectsMalformedMiddleBundle(t *testing.T) {
	p, block := parseReplicaFixture(t, `{
      "height":10,
      "abci_block":{
        "time":"2026-08-08T03:16:34.462864575",
        "round":20,
        "parent_round":19,
        "proposer":"0xproposer",
        "signed_action_bundles":[
          ["ignored",{"signed_actions":[{"action":{"type":"order","orders":[{}]}}]}],
          ["malformed-only-one-element"]
        ]
      }
    }`)
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err == nil || got != nil {
		t.Fatalf("malformed middle bundle produced metrics: got=%+v err=%v", got, err)
	}
}

func TestExtractMetricsBoundsActionsOperationsAndMultiSigInner(t *testing.T) {
	p, block := parseReplicaFixture(t, `{
      "height":100,
      "abci_block":{
        "time":"2026-08-08T03:16:34.462864575Z",
        "round":200,
        "parent_round":198,
        "proposer":"0xproposer",
        "signed_action_bundles":[["ignored",{"signed_actions":[
          {"action":{"type":"order","orders":[{},{}]}},
          {"action":{"type":"future-payload-address","payload":{"secret":"x"}}},
          {"action":{"type":"multiSig","payload":{"action":{"type":"validatorL1Vote"}}}},
          {"action":{"type":"multiSig","payload":{"action":{"type":"unknown-inner"}}}}
        ]}]]
      }
    }`)
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalBundles != 1 || got.TotalActions != 4 || got.TotalOperations != 5 {
		t.Fatalf("totals = bundles:%d actions:%d operations:%d", got.TotalBundles, got.TotalActions, got.TotalOperations)
	}
	if got.ActionCounts["other"] != 1 || got.ParserEvents["unknown_action"] != 1 {
		t.Fatalf("unknown action handling = counts:%#v parser:%#v", got.ActionCounts, got.ParserEvents)
	}
	if got.MultiSigInner["governance"] != 1 || got.MultiSigInner["other"] != 1 || got.ParserEvents["unknown_multisig_inner"] != 1 {
		t.Fatalf("multisig inner handling = inner:%#v parser:%#v", got.MultiSigInner, got.ParserEvents)
	}
}

func TestExtractMetricsClassifiesConstructedTestnetNewActionTypes(t *testing.T) {
	body, err := os.ReadFile("testdata/testnet_new_action_types.constructed.json")
	if err != nil {
		t.Fatal(err)
	}
	p, block := parseReplicaFixture(t, string(body))
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActionCounts["outcomeDeploy"] != 1 || got.OperationCounts["outcomeDeploy"] != 1 {
		t.Fatalf("outcomeDeploy metrics = actions:%d operations:%d", got.ActionCounts["outcomeDeploy"], got.OperationCounts["outcomeDeploy"])
	}
	if got.ActionCounts["trailingStop"] != 1 || got.OperationCounts["trailingStop"] != 1 {
		t.Fatalf("trailingStop metrics = actions:%d operations:%d", got.ActionCounts["trailingStop"], got.OperationCounts["trailingStop"])
	}
	if got.ActionCounts["other"] != 0 || got.ParserEvents["unknown_action"] != 0 {
		t.Fatalf("new official actions remained unknown: actions=%#v parser=%#v", got.ActionCounts, got.ParserEvents)
	}
}

func TestExtractMetricsRejectsZeroActionBundleAndMissingDiscriminant(t *testing.T) {
	tests := []string{
		`[["id",{"signed_actions":[]}]]`,
		`[["id",{"signed_actions":[{"action":{"orders":[]}}]}]]`,
		`[["id",{"signed_actions":[{"action":{"type":"order"}}]}]]`,
	}
	for _, bundles := range tests {
		p, block := parseReplicaFixture(t, `{"height":1,"abci_block":{"time":"2026-08-08T03:16:34.462864575","round":2,"proposer":"p","signed_action_bundles":`+bundles+`}}`)
		got, err := p.ExtractMetrics(block)
		p.ReturnBlock(block)
		if err == nil || got != nil {
			t.Fatalf("invalid bundles accepted: %s", bundles)
		}
	}
}

func TestExtractMetricsClassifiesCDeposit2(t *testing.T) {
	p, block := parseReplicaFixture(t, `{"height":100,"abci_block":{"time":"2026-10-26T06:28:13Z","round":200,"proposer":"constructed","signed_action_bundles":[["bundle",{"signed_actions":[{"action":{"type":"cDeposit2","wei":1}},{"action":{"type":"cDeposit3","wei":1}}]}]]}}`)
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"cDeposit2": 1, "other": 1}
	if !reflect.DeepEqual(got.ActionCounts, want) || !reflect.DeepEqual(got.OperationCounts, want) || got.TotalActions != 2 || got.TotalOperations != 2 || !reflect.DeepEqual(got.ParserEvents, map[string]int{"unknown_action": 1}) {
		t.Fatalf("cDeposit2 classification or unknown-action reporting is incorrect: %+v", got)
	}
}

func TestExtractMetricsClassifiesVoteAbciDigest(t *testing.T) {
	p, block := parseReplicaFixture(t, `{"height":100,"abci_block":{"time":"2026-10-02T08:15:10Z","round":200,"proposer":"0x1111..1111","signed_action_bundles":[["bundle",{"signed_actions":[{"action":{"type":"voteAbciDigest","height":100,"abciDigest":"constructed","signature":"constructed"}},{"action":{"type":"unrecognizedFutureAction"}}]}]]}}`)
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"voteAbciDigest": 1, "other": 1}
	if !reflect.DeepEqual(got.ActionCounts, want) || !reflect.DeepEqual(got.OperationCounts, want) || got.TotalActions != 2 || got.TotalOperations != 2 || got.ParserEvents["unknown_action"] != 1 {
		t.Fatalf("voteAbciDigest classification or unknown-action reporting is incorrect: %+v", got)
	}
}

func TestExtractMetricsClassifiesValidatorL1StatusAndRetainsUnknownActions(t *testing.T) {
	body, err := os.ReadFile("testdata/validator_l1_status.constructed.json")
	if err != nil {
		t.Fatal(err)
	}
	p, block := parseReplicaFixture(t, string(body))
	defer p.ReturnBlock(block)
	got, err := p.ExtractMetrics(block)
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"validatorL1Status": 1, "other": 1}
	if !reflect.DeepEqual(got.ActionCounts, wantCounts) || !reflect.DeepEqual(got.OperationCounts, wantCounts) {
		t.Fatalf("action counts = %#v, operation counts = %#v, want %#v", got.ActionCounts, got.OperationCounts, wantCounts)
	}
	if got.TotalActions != 2 || got.TotalOperations != 2 {
		t.Fatalf("totals = actions:%d operations:%d, want 2 each", got.TotalActions, got.TotalOperations)
	}
	if !reflect.DeepEqual(got.ParserEvents, map[string]int{"unknown_action": 1}) {
		t.Fatalf("parser events = %#v, want only the unrelated unknown action", got.ParserEvents)
	}
}

func TestParseReplicaSetGlobalEmptyData(t *testing.T) {
	for name, response := range map[string]string{
		"empty array": `{"type":"setGlobal","data":[]}`,
		"whitespace":  `{"data":[ ],"type":"setGlobal"}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`{"Full":[["constructed",[{"res":{"status":"ok","response":` + response + `}}]]]}`)
			got := parseReplicaResponses(raw, 1)
			if got.Coverage != "available" || got.Records != 1 || got.CountRelation != "equal" || got.MalformedRecords != 0 || got.MalformedContainers != 0 || !reflect.DeepEqual(got.ActionStatuses, map[string]int{"ok": 1}) || len(got.Outcomes) != 0 {
				t.Fatalf("empty setGlobal response must count only its action status: %+v", got)
			}
		})
	}
	for name, record := range map[string]string{
		"different type": `{"status":"ok","response":{"type":"future","data":[]}}`,
		"missing type":   `{"status":"ok","response":{"data":[]}}`,
		"nonempty array": `{"status":"ok","response":{"type":"setGlobal","data":[{}]}}`,
		"null item":      `{"status":"ok","response":{"type":"setGlobal","data":[null]}}`,
		"wrong data":     `{"status":"ok","response":{"type":"setGlobal","data":true}}`,
		"error status":   `{"status":"err","response":{"type":"setGlobal","data":[]}}`,
		"unknown status": `{"status":"future","response":{"type":"setGlobal","data":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := parseReplicaResponses(json.RawMessage(`{"Full":[["constructed",[{"res":`+record+`}]]]}`), 1)
			if got.Coverage != "malformed" || got.Records != 1 || got.MalformedRecords != 1 || got.MalformedContainers != 0 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 {
				t.Fatalf("unsupported data array must not publish a partial status: %+v", got)
			}
		})
	}
}

func TestParseReplicaResponsesIsOrderIndependentAndCountOnly(t *testing.T) {
	recordA := `{"user":"0xsecret-account","res":{"status":"ok","response":{"type":"order","data":{"statuses":["success",{"resting":{"oid":"secret"}},{"error":"free text"},{"waitingForTrigger":{}},{"future":{"payload":"x"}}]}}}}`
	recordB := `{"user":null,"res":{"status":"future-status","response":"unbounded text ignored"}}`
	rawOne := json.RawMessage(`{"Full":[["0xsecret-bundle",[` + recordA + `,` + recordB + `]]]}`)
	rawTwo := json.RawMessage(`{"Full":[["different-secret",[` + recordB + `,` + recordA + `]]]}`)
	one := parseReplicaResponses(rawOne, 2)
	two := parseReplicaResponses(rawTwo, 2)
	if !reflect.DeepEqual(one, two) {
		t.Fatalf("reordered response metrics differ:\n%+v\n%+v", one, two)
	}
	if one.Coverage != "available" || one.CountRelation != "equal" || one.Records != 2 || one.MalformedRecords != 0 || one.MalformedContainers != 0 {
		t.Fatalf("response coverage = %+v", one)
	}
	if one.ActionStatuses["ok"] != 1 || one.ActionStatuses["other"] != 1 {
		t.Fatalf("action statuses = %#v", one.ActionStatuses)
	}
	wantOutcomes := map[string]int{"success": 1, "resting": 1, "error": 1, "waiting_for_trigger": 1, "other": 1}
	if !reflect.DeepEqual(one.Outcomes, wantOutcomes) {
		t.Fatalf("outcomes = %#v, want %#v", one.Outcomes, wantOutcomes)
	}
}

func TestParseReplicaHashResponsesAreUnavailable(t *testing.T) {
	for _, digest := range []string{strings.Repeat("a1", 32), strings.Repeat("AF", 32)} {
		for _, requests := range []int{0, 2} {
			got := parseReplicaResponses(json.RawMessage(`{"Hash":"0x`+digest+`"}`), requests)
			if got.Coverage != "unavailable" || got.MalformedContainers != 0 || got.MalformedRecords != 0 || got.Records != 0 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 || got.CountRelation != countRelation(requests, 0) {
				t.Fatalf("hash-only response fabricated outcomes or malformed coverage: %+v", got)
			}
		}
	}
}

func TestParseReplicaHashResponsesRejectMalformedContainers(t *testing.T) {
	valid := `"0x` + strings.Repeat("ab", 32) + `"`
	for name, raw := range map[string]string{
		"null":          `{"Hash":null}`,
		"number":        `{"Hash":123}`,
		"array":         `{"Hash":[]}`,
		"empty":         `{"Hash":""}`,
		"short":         `{"Hash":"0xab"}`,
		"long":          `{"Hash":"0x` + strings.Repeat("ab", 33) + `"}`,
		"bare":          `{"Hash":"` + strings.Repeat("ab", 32) + `"}`,
		"nonhex":        `{"Hash":"0x` + strings.Repeat("zz", 32) + `"}`,
		"wrong tag":     `{"hash":` + valid + `}`,
		"mixed wrapper": `{"Hash":` + valid + `,"Full":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := parseReplicaResponses(json.RawMessage(raw), 2)
			if got.Coverage != "malformed" || got.MalformedContainers != 1 || got.MalformedRecords != 0 || got.Records != 0 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 {
				t.Fatalf("malformed hash container accepted: %+v", got)
			}
		})
	}
}

func TestParseReplicaResponsesReportsMalformedAndCountRelation(t *testing.T) {
	got := parseReplicaResponses(json.RawMessage(`{"Full":[["ignored",[{}]]]}`), 2)
	if got.Coverage != "malformed" || got.MalformedRecords != 1 || got.MalformedContainers != 0 || got.Records != 1 || got.CountRelation != "fewer" {
		t.Fatalf("malformed response = %+v", got)
	}
	more := parseReplicaResponses(json.RawMessage(`{"Full":[["ignored",[{"res":{"status":"ok"}},{"res":{"status":"err"}}]]]}`), 1)
	if more.CountRelation != "more" || more.Records != 2 {
		t.Fatalf("more response relation = %+v", more)
	}
	for _, record := range []string{
		`{"res":{"status":null}}`,
		`{"res":{"status":"ok","response":{"data":{"statuses":null}}}}`,
		`{"res":{"status":"ok","response":{"data":{"statuses":["success",null]}}}}`,
	} {
		got := parseReplicaResponses(json.RawMessage(`{"Full":[["ignored",[`+record+`]]]}`), 1)
		if got.Coverage != "malformed" || got.MalformedRecords != 1 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 {
			t.Fatalf("required-null response leaked partial metrics: record=%s metrics=%+v", record, got)
		}
	}
	for name, raw := range map[string]string{
		"null Full":    `{"Full":null}`,
		"null records": `{"Full":[["ignored",null]]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := parseReplicaResponses(json.RawMessage(raw), 1)
			if got.Coverage != "malformed" || got.MalformedContainers != 1 || got.Records != 0 || len(got.ActionStatuses) != 0 || len(got.Outcomes) != 0 {
				t.Fatalf("wrong-shape null response = %+v", got)
			}
		})
	}
}

func TestClassifyErrorUsesOnlyBoundedStageAndReason(t *testing.T) {
	p, block := parseReplicaFixture(t, `{"height":1,"abci_block":{"time":"2026-08-08T03:16:34Z","round":2,"proposer":"p","signed_action_bundles":[["secret",{"signed_actions":[{"action":{"type":"order"}}]}]]}}`)
	defer p.ReturnBlock(block)
	_, err := p.ExtractMetrics(block)
	if err == nil {
		t.Fatal("expected schema error")
	}
	stage, reason := ClassifyError(err)
	if stage != "operation" || reason != "missing_orders" {
		t.Fatalf("classification = %q/%q, err=%v", stage, reason, err)
	}
	if stage == err.Error() || reason == err.Error() {
		t.Fatal("classification leaked dynamic error text")
	}
}
