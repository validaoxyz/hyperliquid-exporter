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
