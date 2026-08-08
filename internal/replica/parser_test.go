package replica

import (
	"encoding/json"
	"reflect"
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

func TestParseReplicaResponsesReportsMalformedAndCountRelation(t *testing.T) {
	got := parseReplicaResponses(json.RawMessage(`{"Full":[["ignored",[{}]]]}`), 2)
	if got.Coverage != "malformed" || got.MalformedRecords != 1 || got.MalformedContainers != 0 || got.Records != 1 || got.CountRelation != "fewer" {
		t.Fatalf("malformed response = %+v", got)
	}
	more := parseReplicaResponses(json.RawMessage(`{"Full":[["ignored",[{"res":{"status":"ok"}},{"res":{"status":"err"}}]]]}`), 1)
	if more.CountRelation != "more" || more.Records != 2 {
		t.Fatalf("more response relation = %+v", more)
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
