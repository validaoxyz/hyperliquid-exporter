package evm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	zeroAddress = "0x0000000000000000000000000000000000000000"
	oneAddress  = "0x1111111111111111111111111111111111111111"
)

func TestParseTimestampVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value string
		want  string
		nanos int
	}{
		{name: "zoneless nanoseconds", value: "2026-08-08T03:15:14.210165152", want: "2026-08-08T03:15:14.210165152Z", nanos: 210165152},
		{name: "UTC Z", value: "2026-08-08T03:15:14.210165152Z", want: "2026-08-08T03:15:14.210165152Z", nanos: 210165152},
		{name: "explicit offset", value: "2026-08-08T10:15:14.210165152+07:00", want: "2026-08-08T03:15:14.210165152Z", nanos: 210165152},
		{name: "zoneless DST date remains UTC", value: "2026-11-01T01:30:00.5", want: "2026-11-01T01:30:00.5Z", nanos: 500000000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTimestamp(tc.value)
			if err != nil {
				t.Fatalf("ParseTimestamp: %v", err)
			}
			if got.Format(time.RFC3339Nano) != tc.want || got.Nanosecond() != tc.nanos || got.Location() != time.UTC {
				t.Fatalf("got %s (%d, %v), want %s (%d, UTC)", got.Format(time.RFC3339Nano), got.Nanosecond(), got.Location(), tc.want, tc.nanos)
			}
		})
	}
}

func TestParseTimestampRejectsMalformed(t *testing.T) {
	t.Parallel()
	_, err := ParseTimestamp("2026-08-08 03:15:14")
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Stage != StageTimestamp || parseErr.Reason != ReasonInvalidValue {
		t.Fatalf("got %v", err)
	}
}

// This sanitized fixture pins the live two-element wrapper observed on the
// testnet node built 2026-08-07; synthetic fixtures exercise branches that were
// absent from that sample.
func TestCurrentTestnetFixture(t *testing.T) {
	t.Parallel()
	line, err := os.ReadFile("testdata/testnet_hl-node_2026-08-07_current.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ParseLine(line, FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if obs.Height != 60988464 || obs.TransactionCount != 2 || obs.ReceiptCount != 2 || obs.ReceiptCountMismatch {
		t.Fatalf("unexpected coverage: height=%d tx=%d receipts=%d mismatch=%v", obs.Height, obs.TransactionCount, obs.ReceiptCount, obs.ReceiptCountMismatch)
	}
	if obs.Transactions[0].Shape != TxShapeCreate || obs.Transactions[1].Shape != TxShapeMessage {
		t.Fatalf("unexpected shapes: %q/%q", obs.Transactions[0].Shape, obs.Transactions[1].Shape)
	}
	if obs.ReceiptOutcomes != (OutcomeCounts{Success: 1, Failed: 1}) || obs.PrecompileOutcomes != (PrecompileOutcomeCounts{OK: 1}) {
		t.Fatalf("outcomes receipts=%+v precompiles=%+v", obs.ReceiptOutcomes, obs.PrecompileOutcomes)
	}
}

func TestDestinationShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		toJSON    string
		wantShape string
		wantAddr  string
	}{
		{name: "null is creation", toJSON: "null", wantShape: TxShapeCreate},
		{name: "zero is an ordinary message", toJSON: fmt.Sprintf("%q", zeroAddress), wantShape: TxShapeMessage, wantAddr: zeroAddress},
		{name: "canonical uppercase recipient", toJSON: fmt.Sprintf("%q", strings.ToUpper(oneAddress)), wantShape: TxShapeMessage, wantAddr: oneAddress},
		{name: "canonical lowercase recipient", toJSON: fmt.Sprintf("%q", oneAddress), wantShape: TxShapeMessage, wantAddr: oneAddress},
		{name: "missing is unknown", toJSON: "", wantShape: TxShapeUnknown},
		{name: "wrong type is unknown", toJSON: "42", wantShape: TxShapeUnknown},
		{name: "malformed string is unknown", toJSON: `"0x1234"`, wantShape: TxShapeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{
				"maxPriorityFeePerGas": `"0x0"`,
				"maxFeePerGas":         `"0x0"`,
			}
			if tc.toJSON != "" {
				fields["to"] = tc.toJSON
			}
			tx := transactionJSON(TxTypeEIP1559, fields)
			obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", []string{tx}, `[{"success":true}]`, `[]`, `[]`, `null`)), FullOptions())
			if err != nil {
				t.Fatalf("ParseLine: %v", err)
			}
			got := obs.Transactions[0]
			if got.Shape != tc.wantShape || got.Recipient != tc.wantAddr {
				t.Fatalf("got shape/address %q/%q, want %q/%q", got.Shape, got.Recipient, tc.wantShape, tc.wantAddr)
			}
		})
	}
}

func TestReceiptCreatedAddressDoesNotBecomeAnIdentityLabel(t *testing.T) {
	t.Parallel()
	tx := transactionJSON(TxTypeEIP1559, map[string]string{
		"maxPriorityFeePerGas": `"0x0"`,
		"maxFeePerGas":         `"0x0"`,
		"to":                   `null`,
	})
	receipts := fmt.Sprintf(`[{"success":true,"contractAddress":%q}]`, oneAddress)
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", []string{tx}, receipts, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if obs.Transactions[0].Shape != TxShapeCreate || obs.Transactions[0].Recipient != "" {
		t.Fatalf("receipt metadata changed score identity: %+v", obs.Transactions[0])
	}
}

func TestEmptyBlockAndZeroFeesAreValid(t *testing.T) {
	t.Parallel()
	empty, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", nil, `[]`, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("empty block: %v", err)
	}
	if empty.TransactionCount != 0 || empty.HasPriorityFee || empty.BaseFee.Sign() != 0 {
		t.Fatalf("unexpected empty observation: %+v", empty)
	}

	zeroFeeTx := transactionJSON(TxTypeLegacy, map[string]string{
		"gasPrice": `"0x0"`,
		"to":       fmt.Sprintf("%q", oneAddress),
	})
	nonempty, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:15.1", "0x0", "0x0", "0x0", []string{zeroFeeTx}, `[{"success":true}]`, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("zero fee block: %v", err)
	}
	if nonempty.TransactionCount != 1 || !nonempty.HasPriorityFee || nonempty.MaxPriorityFeeWei.Sign() != 0 {
		t.Fatalf("unexpected zero-fee observation: %+v", nonempty)
	}
}

func TestReceiptOutcomesAndCountMismatch(t *testing.T) {
	t.Parallel()
	txs := []string{
		transactionJSON(TxTypeLegacy, map[string]string{"gasPrice": `"0x0"`, "to": fmt.Sprintf("%q", oneAddress)}),
		transactionJSON(TxTypeLegacy, map[string]string{"gasPrice": `"0x0"`, "to": fmt.Sprintf("%q", oneAddress)}),
	}
	receipts := `[{"success":true},{"success":false},{},{"success":null},{"success":"true"}]`
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", txs, receipts, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if obs.ReceiptOutcomes != (OutcomeCounts{Success: 1, Failed: 1, Unknown: 3}) {
		t.Fatalf("outcomes = %+v", obs.ReceiptOutcomes)
	}
	if obs.ReceiptCount != 5 || !obs.ReceiptCountMismatch {
		t.Fatalf("count=%d mismatch=%v", obs.ReceiptCount, obs.ReceiptCountMismatch)
	}
}

func TestReceiptCountRelationsBothDirections(t *testing.T) {
	t.Parallel()
	tx := transactionJSON(TxTypeLegacy, map[string]string{"gasPrice": `"0x0"`, "to": fmt.Sprintf("%q", oneAddress)})
	cases := []struct {
		name     string
		txs      []string
		receipts string
		mismatch bool
	}{
		{name: "both empty", txs: nil, receipts: `[]`, mismatch: false},
		{name: "equal", txs: []string{tx}, receipts: `[{"success":true}]`, mismatch: false},
		{name: "fewer receipts", txs: []string{tx}, receipts: `[]`, mismatch: true},
		{name: "more receipts", txs: nil, receipts: `[{"success":true}]`, mismatch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", tc.txs, tc.receipts, `[]`, `[]`, `null`)), FullOptions())
			if err != nil {
				t.Fatalf("ParseLine: %v", err)
			}
			if obs.ReceiptCountMismatch != tc.mismatch {
				t.Fatalf("mismatch=%v, want %v", obs.ReceiptCountMismatch, tc.mismatch)
			}
		})
	}
}

func TestRequiredConsumedArraysFailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		field string
		value string
		stage ParseStage
	}{
		{name: "receipts missing", field: "receipts", value: "__MISSING__", stage: StageReceipts},
		{name: "receipts null", field: "receipts", value: `null`, stage: StageReceipts},
		{name: "receipts object", field: "receipts", value: `{}`, stage: StageReceipts},
		{name: "system null", field: "system_txs", value: `null`, stage: StageSystemTxs},
		{name: "system missing", field: "system_txs", value: "__MISSING__", stage: StageSystemTxs},
		{name: "system object", field: "system_txs", value: `{}`, stage: StageSystemTxs},
		{name: "calls null", field: "read_precompile_calls", value: `null`, stage: StagePrecompileCalls},
		{name: "calls missing", field: "read_precompile_calls", value: "__MISSING__", stage: StagePrecompileCalls},
		{name: "calls object", field: "read_precompile_calls", value: `{}`, stage: StagePrecompileCalls},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := customPayloadLine(tc.field, tc.value)
			_, err := ParseLine([]byte(line), FullOptions())
			var parseErr *ParseError
			if !errors.As(err, &parseErr) || parseErr.Stage != tc.stage {
				t.Fatalf("got %v, want stage %s", err, tc.stage)
			}
		})
	}
}

func TestSystemTransactionsAreOpaqueItems(t *testing.T) {
	t.Parallel()
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", nil, `[]`, `[null,{"future":{"shape":[1,2,3]}}]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if obs.SystemTxCount != 2 {
		t.Fatalf("system count = %d", obs.SystemTxCount)
	}
}

func TestNestedPrecompileOutcomes(t *testing.T) {
	t.Parallel()
	calls := make([]string, 0, 225)
	for i := 0; i < 225; i++ {
		result := `{"Ok":{"gas_used":1,"bytes":"0x"}}`
		if i == 223 {
			result = `{"Err":{"future":"shape"}}`
		}
		if i == 224 {
			result = `null`
		}
		calls = append(calls, fmt.Sprintf(`[{"input":"0x","gas_limit":10},%s]`, result))
	}
	precompiles := fmt.Sprintf(`[[%q,[%s]]]`, oneAddress, strings.Join(calls, ","))
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", nil, `[]`, `[]`, precompiles, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if obs.PrecompileOutcomes != (PrecompileOutcomeCounts{OK: 223, Other: 2}) {
		t.Fatalf("outcomes = %+v", obs.PrecompileOutcomes)
	}
}

func TestRejectsEmptyTransactionVariantAndInvalidPrecompileAddress(t *testing.T) {
	t.Parallel()
	emptyVariant := transactionJSON("", map[string]string{"to": fmt.Sprintf("%q", oneAddress)})
	_, err := ParseLine([]byte(lineJSON(
		"2026-08-08T03:15:14.1", "0x0", "0x0", "0x0",
		[]string{emptyVariant}, `[{"success":true}]`, `[]`, `[]`, `null`,
	)), FullOptions())
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Stage != StageTransactions || parseErr.Reason != ReasonInvalidValue {
		t.Fatalf("empty transaction variant error = %v", err)
	}

	for name, address := range map[string]string{
		"null":       `null`,
		"empty":      `""`,
		"non-string": `7`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := `[[` + address + `,[[{"input":"0x"},null]]]]`
			_, err := ParseLine([]byte(lineJSON(
				"2026-08-08T03:15:14.1", "0x0", "0x0", "0x0",
				nil, `[]`, `[]`, calls, `null`,
			)), FullOptions())
			var parseErr *ParseError
			if !errors.As(err, &parseErr) || parseErr.Stage != StagePrecompileCalls {
				t.Fatalf("invalid address %s error = %v", address, err)
			}
		})
	}
}

func TestDisabledSiblingsRemainUnparsed(t *testing.T) {
	t.Parallel()
	line := lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", nil, `{"not":"an array"}`, `null`, `42`, `{"large":[1,2,3]}`)
	obs, err := ParseLine([]byte(line), Options{})
	if err != nil {
		t.Fatalf("disabled siblings affected block parse: %v", err)
	}
	if obs.ReceiptsEnabled || obs.SystemTxsEnabled || obs.PrecompileCallsEnabled {
		t.Fatalf("disabled flags leaked: %+v", obs)
	}
	if _, err := ParseLine([]byte(line), Options{Receipts: true}); err == nil {
		t.Fatal("malformed consumed receipts unexpectedly accepted")
	}
}

func TestBoundedTransactionTypesAndExactFeeFallbacks(t *testing.T) {
	t.Parallel()
	txs := []string{
		transactionJSON(TxTypeEIP1559, map[string]string{
			"maxPriorityFeePerGas": `"0x5"`,
			"maxFeePerGas":         `"0x67"`, // base 100 -> headroom 3
			"to":                   fmt.Sprintf("%q", oneAddress),
		}),
		transactionJSON(TxTypeLegacy, map[string]string{
			"gasPrice": `"0x6b"`, // base 100 -> tip 7
			"to":       fmt.Sprintf("%q", oneAddress),
		}),
		transactionJSON(TxTypeEIP2930, map[string]string{
			"gasPrice": `"0x68"`, // base 100 -> tip 4
			"to":       fmt.Sprintf("%q", oneAddress),
		}),
		transactionJSON("Eip7702", map[string]string{
			"futureFee": `"0xffff"`,
			"to":        fmt.Sprintf("%q", oneAddress),
		}),
	}
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x64", "0x0", "0x0", txs, `[{"success":true},{"success":true},{"success":true},{"success":true}]`, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if got := obs.MaxPriorityFeeWei.String(); got != "7" {
		t.Fatalf("max priority fee = %s, want 7", got)
	}
	wantTypes := []string{TxTypeEIP1559, TxTypeLegacy, TxTypeEIP2930, TxTypeOther}
	for i, want := range wantTypes {
		if obs.Transactions[i].Type != want {
			t.Fatalf("tx %d type=%q, want %q", i, obs.Transactions[i].Type, want)
		}
	}
}

func TestReceiptEffectivePriceTakesBlockWidePrecedence(t *testing.T) {
	t.Parallel()
	tx := transactionJSON(TxTypeEIP1559, map[string]string{
		"maxPriorityFeePerGas": `"0x1"`,
		"maxFeePerGas":         `"0x65"`,
		"to":                   fmt.Sprintf("%q", oneAddress),
	})
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x64", "0x0", "0x0", []string{tx}, `[{"success":true,"effectiveGasPrice":"0x6e"}]`, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if got := obs.MaxPriorityFeeWei.String(); got != "10" {
		t.Fatalf("effective priority = %s, want 10", got)
	}
}

func TestReceiptEffectivePriceAcceptsExactDecimalInteger(t *testing.T) {
	t.Parallel()
	tx := transactionJSON(TxTypeEIP1559, map[string]string{
		"maxPriorityFeePerGas": `"0x1"`,
		"maxFeePerGas":         `"0x65"`,
		"to":                   fmt.Sprintf("%q", oneAddress),
	})
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x64", "0x0", "0x0", []string{tx}, `[{"success":true,"effective_gas_price":110}]`, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if got := obs.MaxPriorityFeeWei.String(); got != "10" {
		t.Fatalf("effective priority = %s, want 10", got)
	}
}

func TestNullReceiptItemIsMalformed(t *testing.T) {
	t.Parallel()
	_, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", nil, `[null]`, `[]`, `[]`, `null`)), FullOptions())
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Stage != StageReceipts || parseErr.Reason != ReasonWrongType {
		t.Fatalf("got %v", err)
	}
}

func TestLargeIntegerFeesAndOutOfRange(t *testing.T) {
	t.Parallel()
	large := transactionJSON(TxTypeLegacy, map[string]string{
		"gasPrice": `"0x8000000000000000"`,
		"to":       fmt.Sprintf("%q", oneAddress),
	})
	obs, err := ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", []string{large}, `[{"success":true}]`, `[]`, `[]`, `null`)), FullOptions())
	if err != nil {
		t.Fatalf("large uint64 fee should not overflow int64 parser: %v", err)
	}
	if obs.MaxPriorityFeeWei.Cmp(new(big.Int).Lsh(big.NewInt(1), 63)) != 0 {
		t.Fatalf("large fee = %s", obs.MaxPriorityFeeWei)
	}

	tooLargeHex := "0x1" + strings.Repeat("0", 64)
	overflow := transactionJSON(TxTypeLegacy, map[string]string{
		"gasPrice": fmt.Sprintf("%q", tooLargeHex),
		"to":       fmt.Sprintf("%q", oneAddress),
	})
	_, err = ParseLine([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", []string{overflow}, `[{"success":true}]`, `[]`, `[]`, `null`)), FullOptions())
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Reason != ReasonOutOfRange {
		t.Fatalf("got %v, want out_of_range", err)
	}
}

func TestGasValues(t *testing.T) {
	t.Parallel()
	used, limit, ratio, err := GasValues(big.NewInt(1_500_000), big.NewInt(3_000_000))
	if err != nil || used != 1_500_000 || limit != 3_000_000 || ratio == nil || *ratio != 0.5 {
		t.Fatalf("got used=%v limit=%v ratio=%v err=%v", used, limit, ratio, err)
	}
	_, _, ratio, err = GasValues(big.NewInt(0), big.NewInt(0))
	if err != nil || ratio != nil {
		t.Fatalf("zero limit ratio=%v err=%v", ratio, err)
	}
	if _, _, _, err := GasValues(big.NewInt(2), big.NewInt(1)); err == nil {
		t.Fatal("gasUsed > gasLimit accepted")
	}
	for _, gasLimit := range []int64{2_000_000, 3_000_000, 30_000_000, 31_000_001} {
		_, gotLimit, gotRatio, err := GasValues(big.NewInt(gasLimit/4), big.NewInt(gasLimit))
		if err != nil || gotLimit != float64(gasLimit) || gotRatio == nil || *gotRatio < 0 || *gotRatio > 1 {
			t.Fatalf("gas limit %d: limit=%f ratio=%v err=%v", gasLimit, gotLimit, gotRatio, err)
		}
	}
}

func TestWeiToGwei(t *testing.T) {
	t.Parallel()
	got, err := WeiToGwei(big.NewInt(100_000_001))
	if err != nil || math.Abs(got-0.100000001) > 1e-15 {
		t.Fatalf("got %.12f, err=%v", got, err)
	}
}

func BenchmarkParseLineLargeIgnoredReceiptLogs(b *testing.B) {
	largeLog := strings.Repeat("ab", 1<<20)
	tx := transactionJSON(TxTypeLegacy, map[string]string{"gasPrice": `"0x0"`, "to": fmt.Sprintf("%q", oneAddress)})
	receipts := fmt.Sprintf(`[{"success":true,"logs":[{"data":%q}]}]`, largeLog)
	line := []byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", []string{tx}, receipts, `[]`, `[]`, `null`))
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseLine(line, FullOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzParseLineKeepsBoundedSemanticVocabularies(f *testing.F) {
	f.Add([]byte(lineJSON("2026-08-08T03:15:14.1", "0x0", "0x0", "0x0", nil, `[]`, `[]`, `[]`, `null`)))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, line []byte) {
		obs, err := ParseLine(line, FullOptions())
		if err != nil {
			return
		}
		if obs.TransactionCount != len(obs.Transactions) || obs.TransactionCount < 0 || obs.ReceiptCount < 0 || obs.SystemTxCount < 0 {
			t.Fatalf("invalid counts in accepted observation: %+v", obs)
		}
		for _, tx := range obs.Transactions {
			switch tx.Type {
			case TxTypeEIP1559, TxTypeLegacy, TxTypeEIP2930, TxTypeOther:
			default:
				t.Fatalf("unbounded transaction type %q", tx.Type)
			}
			switch tx.Shape {
			case TxShapeCreate, TxShapeMessage, TxShapeUnknown:
			default:
				t.Fatalf("unbounded transaction shape %q", tx.Shape)
			}
		}
	})
}

func transactionJSON(txType string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	// Stable fixture bytes make benchmark and failure output reproducible.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%q:%s", key, fields[key]))
	}
	return fmt.Sprintf(`{"transaction":{%q:{%s}},"signature":{"r":"0x0","s":"0x0"}}`, txType, strings.Join(parts, ","))
}

func lineJSON(timestamp, baseFee, gasUsed, gasLimit string, txs []string, receipts, systemTxs, precompileCalls, ignored string) string {
	if txs == nil {
		txs = []string{}
	}
	payload := fmt.Sprintf(
		`{"block":{"Reth115":{"header":{"hash":"0x0","header":{"number":"0x2a","gasLimit":%q,"gasUsed":%q,"timestamp":"0x0","baseFeePerGas":%q}},"body":{"transactions":[%s]}}},"receipts":%s,"system_txs":%s,"read_precompile_calls":%s,"highest_precompile_address":%s}`,
		gasLimit,
		gasUsed,
		baseFee,
		strings.Join(txs, ","),
		receipts,
		systemTxs,
		precompileCalls,
		ignored,
	)
	return fmt.Sprintf(`[%q,%s]`, timestamp, payload)
}

func customPayloadLine(field, value string) string {
	fields := map[string]string{
		"receipts":              `[]`,
		"system_txs":            `[]`,
		"read_precompile_calls": `[]`,
	}
	if value == "__MISSING__" {
		delete(fields, field)
	} else {
		fields[field] = value
	}
	block := `{"Reth115":{"header":{"header":{"number":"0x2a","gasLimit":"0x0","gasUsed":"0x0","baseFeePerGas":"0x0"}},"body":{"transactions":[]}}}`
	payload := map[string]json.RawMessage{"block": json.RawMessage(block)}
	for key, raw := range fields {
		payload[key] = json.RawMessage(raw)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`[%q,%s]`, "2026-08-08T03:15:14.1", payloadBytes)
}
