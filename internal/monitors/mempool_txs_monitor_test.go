package monitors

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMempoolTxsLine(t *testing.T) {
	line := []byte(`["2026-06-01T17:59:25.990970382",{"tx_hash":"0xabc","signed_actions":[{"action":{"type":"order","orders":[{"b":true,"t":{"limit":{"tif":"Alo"}}},{"b":false,"t":{"limit":{"tif":"Ioc"}}}]}},{"action":{"type":"batchModify","modifies":[{"order":{"b":true,"t":{"limit":{"tif":"Gtc"}}}},{"order":{"b":false,"t":{"limit":{"tif":"FrontendMarket"}}}}]}},{"action":{"type":"cancel","cancels":[{"a":1,"o":2},{"a":2,"o":3}]}},{"action":{"type":"futureAction"}}]}]`)

	stats, ok := parseMempoolTxsLine(line)
	if !ok {
		t.Fatal("parseMempoolTxsLine returned !ok")
	}

	if stats.timestamp.IsZero() {
		t.Fatal("timestamp was not parsed")
	}
	if stats.signedActions != 4 {
		t.Fatalf("signedActions = %d, want 4", stats.signedActions)
	}
	if stats.operations != 7 {
		t.Fatalf("operations = %d, want 7", stats.operations)
	}

	wantActions := map[string]int{
		"order":       1,
		"batchModify": 1,
		"cancel":      1,
		"other":       1,
	}
	for actionType, want := range wantActions {
		if got := stats.actionCounts[actionType]; got != want {
			t.Fatalf("actionCounts[%q] = %d, want %d", actionType, got, want)
		}
	}
	if got := stats.operationCounts["order"]; got != 2 {
		t.Fatalf("operationCounts[order] = %d, want 2", got)
	}
	if got := stats.operationCounts["batchModify"]; got != 2 {
		t.Fatalf("operationCounts[batchModify] = %d, want 2", got)
	}
	if got := stats.operationCounts["cancel"]; got != 2 {
		t.Fatalf("operationCounts[cancel] = %d, want 2", got)
	}
	if got := stats.operationCounts["other"]; got != 1 {
		t.Fatalf("operationCounts[other] = %d, want 1", got)
	}

	wantOrders := map[mempoolTxOrderLabel]int{
		{side: "buy", tif: "Alo"}:             1,
		{side: "sell", tif: "Ioc"}:            1,
		{side: "buy", tif: "Gtc"}:             1,
		{side: "sell", tif: "FrontendMarket"}: 1,
	}
	for label, want := range wantOrders {
		if got := stats.orderCounts[label]; got != want {
			t.Fatalf("orderCounts[%+v] = %d, want %d", label, got, want)
		}
	}
}

func TestReadMempoolTxsEventsDoesNotConsumePartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mempool_txs")
	line := `["2026-06-01T17:59:25.990970382",{"tx_hash":"0xabc","signed_actions":[{"action":{"type":"noop"}}]}]`

	if err := os.WriteFile(path, []byte(line+"\n"+line), 0o600); err != nil {
		t.Fatal(err)
	}

	offset, n, err := readMempoolTxsEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("processed lines = %d, want 1", n)
	}
	wantOffset := int64(len(line) + 1)
	if offset != wantOffset {
		t.Fatalf("offset = %d, want %d", offset, wantOffset)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	offset, n, err = readMempoolTxsEvents(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("processed lines after completing partial = %d, want 1", n)
	}
	if offset != int64(len(line)*2+2) {
		t.Fatalf("final offset = %d, want %d", offset, len(line)*2+2)
	}
}

func TestParseMempoolTxsTriggerAndUnknownFields(t *testing.T) {
	line := []byte(`["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":"order","orders":[{"b":true,"t":{"trigger":{"isMarket":true,"triggerPx":"10","tpsl":"tp"}}},{"b":false,"t":{"limit":{"tif":"FutureTIF"}}},{"b":true,"t":{"limit":{"tif":null}}}]}},{"action":{"type":"futureAction"}}]}]`)
	stats, reason, ok := parseMempoolTxsLineDetailed(line)
	if !ok || reason != "" {
		t.Fatalf("parse = ok:%v reason:%q", ok, reason)
	}
	if got := stats.orderCounts[mempoolTxOrderLabel{side: "buy", tif: "trigger"}]; got != 1 {
		t.Fatalf("trigger orders = %d, want 1", got)
	}
	if got := stats.orderCounts[mempoolTxOrderLabel{side: "sell", tif: "other"}]; got != 1 {
		t.Fatalf("unknown TIF orders = %d, want 1", got)
	}
	if got := stats.orderCounts[mempoolTxOrderLabel{side: "buy", tif: "unknown"}]; got != 1 {
		t.Fatalf("null TIF orders = %d, want 1", got)
	}
	if stats.parserEvents["unknown_tif"] != 1 || stats.parserEvents["null_tif"] != 1 || stats.parserEvents["unknown_action"] != 1 {
		t.Fatalf("parser events = %#v", stats.parserEvents)
	}
	if stats.actionCounts["other"] != 1 {
		t.Fatalf("unknown action was not bounded: %#v", stats.actionCounts)
	}
}

func TestParseMempoolTxsRejectsIncompleteEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		reason string
	}{
		{name: "invalid envelope", line: `{}`, reason: "invalid_envelope"},
		{name: "invalid timestamp", line: `[7,{"signed_actions":[]}]`, reason: "invalid_timestamp"},
		{name: "missing actions", line: `["2026-06-01T17:59:25.990970382",{}]`, reason: "missing_signed_actions"},
		{name: "null actions", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":null}]`, reason: "missing_signed_actions"},
		{name: "null action row", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[null]}]`, reason: "invalid_signed_action"},
		{name: "null action payload", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":null}]}]`, reason: "invalid_signed_action"},
		{name: "wrong actions", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":{}}]`, reason: "invalid_signed_actions"},
		{name: "empty actions", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[]}]`, reason: "invalid_signed_actions"},
		{name: "missing action type", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{}}]}]`, reason: "invalid_signed_action"},
		{name: "null action type", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":null}}]}]`, reason: "invalid_signed_action"},
		{name: "empty action type", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":""}}]}]`, reason: "invalid_signed_action"},
		{name: "blank action type", line: `["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":"  "}}]}]`, reason: "invalid_signed_action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, reason, ok := parseMempoolTxsLineDetailed([]byte(tt.line))
			if ok || reason != tt.reason {
				t.Fatalf("parse = ok:%v reason:%q, want false/%q", ok, reason, tt.reason)
			}
		})
	}
}

func TestMempoolTxsOperationCountsAreOwnedByActionType(t *testing.T) {
	for actionType, field := range map[string]string{
		"order":         `"orders":[]`,
		"cancel":        `"cancels":[]`,
		"cancelByCloid": `"cancels":[]`,
		"batchModify":   `"modifies":[]`,
	} {
		t.Run(actionType, func(t *testing.T) {
			line := []byte(`["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":"` + actionType + `",` + field + `}}]}]`)
			stats, reason, ok := parseMempoolTxsLineDetailed(line)
			if !ok || reason != "" {
				t.Fatalf("empty owned array rejected: ok=%v reason=%q", ok, reason)
			}
			if stats.operations != 0 || stats.operationCounts[actionType] != 0 {
				t.Fatalf("operations=%d by type=%#v, want zero", stats.operations, stats.operationCounts)
			}
		})
	}

	injected := []byte(`["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":"noop","orders":[{"b":true}],"cancels":[{},{}],"modifies":[{"order":{}}],"order":{"b":false}}}]}]`)
	stats, reason, ok := parseMempoolTxsLineDetailed(injected)
	if !ok || reason != "" {
		t.Fatalf("scalar with irrelevant fields rejected: ok=%v reason=%q", ok, reason)
	}
	if stats.operations != 1 || stats.operationCounts["noop"] != 1 || len(stats.orderCounts) != 0 {
		t.Fatalf("irrelevant fields skewed scalar metrics: %+v", stats)
	}
}

func TestMempoolTxsRequiresOwnedActionShapes(t *testing.T) {
	invalidActions := []string{
		`{"type":"order"}`,
		`{"type":"order","orders":null}`,
		`{"type":"order","orders":{}}`,
		`{"type":"order","orders":[null]}`,
		`{"type":"cancel"}`,
		`{"type":"cancel","cancels":null}`,
		`{"type":"cancelByCloid","cancels":[null]}`,
		`{"type":"batchModify"}`,
		`{"type":"batchModify","modifies":[{}]}`,
		`{"type":"modify"}`,
		`{"type":"modify","order":null}`,
	}
	for _, action := range invalidActions {
		line := []byte(`["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":` + action + `}]}]`)
		if _, reason, ok := parseMempoolTxsLineDetailed(line); ok || reason != "invalid_signed_action" {
			t.Fatalf("invalid owned shape accepted: %s: ok=%v reason=%q", action, ok, reason)
		}
	}

	modify := []byte(`["2026-06-01T17:59:25.990970382",{"signed_actions":[{"action":{"type":"modify","order":{"b":true,"t":{"limit":{"tif":"Gtc"}}}}}]}]`)
	stats, reason, ok := parseMempoolTxsLineDetailed(modify)
	if !ok || reason != "" || stats.operations != 1 || stats.orderCounts[mempoolTxOrderLabel{side: "buy", tif: "Gtc"}] != 1 {
		t.Fatalf("valid modify metrics = %+v, ok=%v reason=%q", stats, ok, reason)
	}
}
