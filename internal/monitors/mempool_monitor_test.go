package monitors

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func mempoolLine(payload string) []byte {
	return []byte(`["2026-08-08T03:16:21.000000000",` + payload + `]`)
}

func TestParseMempoolStructuredErrorKinds(t *testing.T) {
	for raw, want := range mempoolErrorKinds {
		t.Run(raw, func(t *testing.T) {
			line := mempoolLine(fmt.Sprintf(`["add_tx","0xsecret",false,"err",{%q:{"address":"0xnever-a-label"}}]`, raw))
			got := parseMempoolObservation(line)
			if !got.complete || got.eventType != "add_tx" || got.status != "err" {
				t.Fatalf("base observation = %+v", got)
			}
			if got.errorOperation != "add_tx" || got.errorKind != want {
				t.Fatalf("structured error = %q/%q, want add_tx/%q", got.errorOperation, got.errorKind, want)
			}
		})
	}

	unknown := parseMempoolObservation(mempoolLine(`["verify_block","0xhash","err",{"FutureKind":"free text"}]`))
	if !unknown.complete || unknown.errorOperation != "verify_block" || unknown.errorKind != "other" {
		t.Fatalf("unknown structured kind = %+v", unknown)
	}

	malformed := parseMempoolObservation(mempoolLine(`["add_tx","0xhash",false,"err",{"A":1,"B":2}]`))
	if malformed.complete || malformed.parseReason != "invalid_error_wrapper" || malformed.eventType != "add_tx" {
		t.Fatalf("malformed wrapper = %+v", malformed)
	}
}

func TestParseMempoolStatusVocabulary(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantStatus string
		wantReason string
		complete   bool
	}{
		{name: "ok", payload: `["add_tx","h",false,"ok"]`, wantStatus: "ok", complete: true},
		{name: "err", payload: `["verify_block","h","err",{"BadBlockRound":{}}]`, wantStatus: "err", complete: true},
		{name: "unknown", payload: `["add_tx","h",false,"future"]`, wantStatus: "other", wantReason: "unknown_status", complete: true},
		{name: "missing", payload: `["add_tx","h",false]`, wantReason: "missing_status"},
		{name: "null", payload: `["verify_block","h",null]`, wantReason: "null_status"},
		{name: "wrong type", payload: `["verify_block","h",7]`, wantReason: "invalid_status_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMempoolObservation(mempoolLine(tt.payload))
			if got.status != tt.wantStatus || got.parseReason != tt.wantReason || got.complete != tt.complete {
				t.Fatalf("status/reason/complete = %q/%q/%v", got.status, got.parseReason, got.complete)
			}
		})
	}
}

func TestParseMempoolPruneDropAndSizePayloads(t *testing.T) {
	prune := parseMempoolObservation(mempoolLine(`["Pruned rpc request throttle",2,1,[]]`))
	if !prune.complete || prune.pruneItems == nil || *prune.pruneItems != 1 {
		t.Fatalf("valid prune = %+v", prune)
	}
	for _, payload := range []string{
		`["Pruned rpc request throttle",2,-1,[]]`,
		`["Pruned rpc request throttle",2,1.5,[]]`,
		`["Pruned rpc request throttle",2,1,["unknown"]]`,
		`["Pruned rpc request throttle",2,1]`,
	} {
		got := parseMempoolObservation(mempoolLine(payload))
		if got.complete || got.pruneItems != nil || got.parseReason != "invalid_prune_payload" {
			t.Fatalf("invalid prune accepted: %+v", got)
		}
	}

	blocks := parseMempoolObservation(mempoolLine(`["dropping blocks",["h1","h2"]]`))
	if !blocks.complete || blocks.droppedKind != "blocks" || blocks.dropped != 2 {
		t.Fatalf("block drop = %+v", blocks)
	}
	txs := parseMempoolObservation(mempoolLine(`["dropping txs",[["2026-08-08T03:16:01.000000000","h1"],["2026-08-08T03:16:02.000000000","h2"]]]`))
	if !txs.complete || txs.droppedKind != "transactions" || txs.dropped != 2 {
		t.Fatalf("tx drop = %+v", txs)
	}

	size := parseMempoolObservation(mempoolLine(`["Size stats",[["committed_tx_hashes",100000],["uncommitted_txs",2],["blocks",3],["rpc_requests",4]],[["secret-hash-1","2026-08-08T03:16:11.000000000"],["secret-hash-2","2026-08-08T03:16:16.000000000"]]]`))
	if !size.complete || size.sizeSnapshot == nil || size.oldestAge == nil || *size.oldestAge != 10 {
		t.Fatalf("size snapshot = %+v", size)
	}
	if size.sizeSnapshot["committed_tx_hashes"] != 100000 || size.sizeSnapshot["uncommitted_txs"] != 2 {
		t.Fatalf("size components = %#v", size.sizeSnapshot)
	}

	badSize := parseMempoolObservation(mempoolLine(`["Size stats",[["committed_tx_hashes",100000],["uncommitted_txs",1],["blocks",3],["rpc_requests",4]],[]]`))
	if badSize.complete || badSize.sizeSnapshot != nil || badSize.parseReason != "invalid_size_snapshot" {
		t.Fatalf("incomplete size snapshot accepted: %+v", badSize)
	}
}

func TestParseMempoolUnknownEventIsBounded(t *testing.T) {
	got := parseMempoolObservation(mempoolLine(`["totally new event",{"payload":"does not become a label"}]`))
	if !got.complete || got.eventType != "other" || got.status != "not_applicable" || got.parseReason != "unknown_event" {
		t.Fatalf("unknown event = %+v", got)
	}
}

func TestReadMempoolEventsDoesNotConsumePartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mempool")
	line := string(mempoolLine(`["add_tx","h",false,"ok"]`))
	if err := os.WriteFile(path, []byte(line+"\n"+line), 0o600); err != nil {
		t.Fatal(err)
	}
	offset, processed, err := readMempoolEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || offset != int64(len(line)+1) {
		t.Fatalf("first read processed/offset = %d/%d", processed, offset)
	}
	appendTestFile(t, path, "\n")
	offset, processed, err = readMempoolEvents(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || offset != int64(len(line)*2+2) {
		t.Fatalf("completed read processed/offset = %d/%d", processed, offset)
	}
}
