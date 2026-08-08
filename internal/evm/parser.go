// Package evm parses the versioned EVM block-and-receipts stream emitted by
// hl-node. It deliberately contains no metric or network dependencies so the
// on-disk schema can be tested independently from exporter state.
package evm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
)

type ParseStage string

const (
	StageEnvelope        ParseStage = "envelope"
	StageTimestamp       ParseStage = "timestamp"
	StagePayload         ParseStage = "payload"
	StageBlock           ParseStage = "block"
	StageHeader          ParseStage = "header"
	StageTransactions    ParseStage = "transactions"
	StageReceipts        ParseStage = "receipts"
	StageSystemTxs       ParseStage = "system_txs"
	StagePrecompileCalls ParseStage = "precompile_calls"
)

type ParseReason string

const (
	ReasonInvalidJSON  ParseReason = "invalid_json"
	ReasonMissing      ParseReason = "missing"
	ReasonWrongType    ParseReason = "wrong_type"
	ReasonInvalidValue ParseReason = "invalid_value"
	ReasonOutOfRange   ParseReason = "out_of_range"
)

// ParseError carries a bounded stage/reason pair for metrics while preserving
// the detailed cause for local logs. Neither label value is derived from the
// source payload.
type ParseError struct {
	Stage  ParseStage
	Reason ParseReason
	Err    error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s/%s: %v", e.Stage, e.Reason, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

func parseError(stage ParseStage, reason ParseReason, format string, args ...interface{}) error {
	return &ParseError{Stage: stage, Reason: reason, Err: fmt.Errorf(format, args...)}
}

type Options struct {
	Receipts        bool
	SystemTxs       bool
	PrecompileCalls bool
}

func FullOptions() Options {
	return Options{
		Receipts:        true,
		SystemTxs:       true,
		PrecompileCalls: true,
	}
}

const (
	TxTypeEIP1559 = "Eip1559"
	TxTypeLegacy  = "Legacy"
	TxTypeEIP2930 = "Eip2930"
	TxTypeOther   = "other"

	TxShapeCreate  = "create"
	TxShapeMessage = "message"
	TxShapeUnknown = "unknown"
)

type Transaction struct {
	Type           string
	Shape          string
	Recipient      string
	PriorityFeeWei *big.Int
	HasPriorityFee bool
}

type OutcomeCounts struct {
	Success uint64
	Failed  uint64
	Unknown uint64
}

type PrecompileOutcomeCounts struct {
	OK    uint64
	Other uint64
}

type Observation struct {
	SourceTime time.Time
	Height     int64
	WrapperTag string

	GasLimit *big.Int
	GasUsed  *big.Int
	BaseFee  *big.Int

	Transactions      []Transaction
	TransactionCount  int
	MaxPriorityFeeWei *big.Int
	HasPriorityFee    bool

	ReceiptsEnabled      bool
	ReceiptCount         int
	ReceiptOutcomes      OutcomeCounts
	ReceiptCountMismatch bool

	SystemTxsEnabled bool
	SystemTxCount    int

	PrecompileCallsEnabled bool
	PrecompileOutcomes     PrecompileOutcomeCounts
}

// ParseLine decodes the outer tuple and only the payload members requested in
// opts. Each payload member remains json.RawMessage until its parser consumes
// it; fields not selected by opts are skipped by encoding/json and are never
// materialized into interface-valued trees.
func ParseLine(line []byte, opts Options) (Observation, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return Observation{}, parseError(StageEnvelope, ReasonInvalidJSON, "decode outer array: %w", err)
	}
	if len(outer) != 2 {
		return Observation{}, parseError(StageEnvelope, ReasonInvalidValue, "expected exactly 2 elements, got %d", len(outer))
	}

	var timestampText string
	if err := json.Unmarshal(outer[0], &timestampText); err != nil {
		return Observation{}, parseError(StageTimestamp, ReasonWrongType, "decode timestamp: %w", err)
	}
	timestamp, err := ParseTimestamp(timestampText)
	if err != nil {
		return Observation{}, err
	}

	members, err := payloadMembers(outer[1], opts)
	if err != nil {
		return Observation{}, err
	}
	obs, err := parseBlock(members.block)
	if err != nil {
		return Observation{}, err
	}
	obs.SourceTime = timestamp

	if opts.Receipts {
		receiptObs, err := parseReceipts(members.receipts, obs.BaseFee)
		if err != nil {
			return Observation{}, err
		}
		obs.ReceiptsEnabled = true
		obs.ReceiptCount = receiptObs.count
		obs.ReceiptOutcomes = receiptObs.outcomes
		obs.ReceiptCountMismatch = obs.TransactionCount != receiptObs.count
		// Receipt effective gas prices are already execution-aware. The stream
		// has no receipt identity, so only the block-wide maximum can be used;
		// no positional transaction/receipt join is performed.
		if receiptObs.hasEffectivePrice {
			if receiptObs.effectivePriceCount == receiptObs.count && receiptObs.count == obs.TransactionCount {
				obs.MaxPriorityFeeWei = receiptObs.maxPriorityFeeWei
			} else if !obs.HasPriorityFee || receiptObs.maxPriorityFeeWei.Cmp(obs.MaxPriorityFeeWei) > 0 {
				obs.MaxPriorityFeeWei = receiptObs.maxPriorityFeeWei
			}
			obs.HasPriorityFee = true
		}
	}

	if opts.SystemTxs {
		items, err := requiredRawArray(members.systemTxs, StageSystemTxs)
		if err != nil {
			return Observation{}, err
		}
		obs.SystemTxsEnabled = true
		obs.SystemTxCount = len(items)
	}

	if opts.PrecompileCalls {
		outcomes, err := parsePrecompileCalls(members.precompileCalls)
		if err != nil {
			return Observation{}, err
		}
		obs.PrecompileCallsEnabled = true
		obs.PrecompileOutcomes = outcomes
	}

	return obs, nil
}

type rawPayloadMembers struct {
	block           json.RawMessage
	receipts        json.RawMessage
	systemTxs       json.RawMessage
	precompileCalls json.RawMessage
}

func payloadMembers(payload json.RawMessage, opts Options) (rawPayloadMembers, error) {
	// The production path consumes all four current members. Decode that shape
	// in one pass; selective/test paths retain one-field projections so disabled
	// siblings are not copied or materialized.
	if opts.Receipts && opts.SystemTxs && opts.PrecompileCalls {
		var p struct {
			Block           json.RawMessage `json:"block"`
			Receipts        json.RawMessage `json:"receipts"`
			SystemTxs       json.RawMessage `json:"system_txs"`
			PrecompileCalls json.RawMessage `json:"read_precompile_calls"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return rawPayloadMembers{}, parseError(StagePayload, ReasonWrongType, "decode payload object: %w", err)
		}
		members := rawPayloadMembers{
			block:           p.Block,
			receipts:        p.Receipts,
			systemTxs:       p.SystemTxs,
			precompileCalls: p.PrecompileCalls,
		}
		for _, required := range []struct {
			name string
			raw  json.RawMessage
		}{
			{name: "block", raw: members.block},
			{name: "receipts", raw: members.receipts},
			{name: "system_txs", raw: members.systemTxs},
			{name: "read_precompile_calls", raw: members.precompileCalls},
		} {
			if len(required.raw) == 0 {
				return rawPayloadMembers{}, parseError(stageForMember(required.name), ReasonMissing, "missing %s", required.name)
			}
		}
		return members, nil
	}

	block, err := payloadMember(payload, "block")
	if err != nil {
		return rawPayloadMembers{}, err
	}
	members := rawPayloadMembers{block: block}
	if opts.Receipts {
		members.receipts, err = payloadMember(payload, "receipts")
		if err != nil {
			return rawPayloadMembers{}, err
		}
	}
	if opts.SystemTxs {
		members.systemTxs, err = payloadMember(payload, "system_txs")
		if err != nil {
			return rawPayloadMembers{}, err
		}
	}
	if opts.PrecompileCalls {
		members.precompileCalls, err = payloadMember(payload, "read_precompile_calls")
		if err != nil {
			return rawPayloadMembers{}, err
		}
	}
	return members, nil
}

// ParseTimestamp accepts the current hl-node zoneless layout as naive UTC and
// RFC3339Nano timestamps carrying either Z or an explicit offset.
func ParseTimestamp(value string) (time.Time, error) {
	if strings.HasSuffix(value, "Z") || hasExplicitOffset(value) {
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, parseError(StageTimestamp, ReasonInvalidValue, "parse RFC3339 timestamp %q: %w", value, err)
		}
		return t.UTC(), nil
	}

	const zonelessNano = "2006-01-02T15:04:05.999999999"
	t, err := time.ParseInLocation(zonelessNano, value, time.UTC)
	if err != nil {
		return time.Time{}, parseError(StageTimestamp, ReasonInvalidValue, "parse zoneless UTC timestamp %q: %w", value, err)
	}
	return t, nil
}

func hasExplicitOffset(value string) bool {
	// The date contributes '-' bytes before the time separator. An RFC3339
	// offset, when present, is the first '+' or '-' after the T.
	t := strings.IndexByte(value, 'T')
	if t < 0 {
		return false
	}
	rest := value[t+1:]
	return strings.ContainsAny(rest, "+-")
}

// payloadMember uses one-field projections so a disabled sibling is skipped,
// rather than copied into another RawMessage or decoded as interface{}.
func payloadMember(payload json.RawMessage, name string) (json.RawMessage, error) {
	var raw json.RawMessage
	switch name {
	case "block":
		var p struct {
			Value json.RawMessage `json:"block"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, parseError(StagePayload, ReasonWrongType, "decode payload object: %w", err)
		}
		raw = p.Value
	case "receipts":
		var p struct {
			Value json.RawMessage `json:"receipts"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, parseError(StagePayload, ReasonWrongType, "decode payload object: %w", err)
		}
		raw = p.Value
	case "system_txs":
		var p struct {
			Value json.RawMessage `json:"system_txs"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, parseError(StagePayload, ReasonWrongType, "decode payload object: %w", err)
		}
		raw = p.Value
	case "read_precompile_calls":
		var p struct {
			Value json.RawMessage `json:"read_precompile_calls"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, parseError(StagePayload, ReasonWrongType, "decode payload object: %w", err)
		}
		raw = p.Value
	default:
		return nil, parseError(StagePayload, ReasonInvalidValue, "unsupported member %q", name)
	}
	if len(raw) == 0 {
		return nil, parseError(stageForMember(name), ReasonMissing, "missing %s", name)
	}
	return raw, nil
}

func stageForMember(name string) ParseStage {
	switch name {
	case "block":
		return StageBlock
	case "receipts":
		return StageReceipts
	case "system_txs":
		return StageSystemTxs
	case "read_precompile_calls":
		return StagePrecompileCalls
	default:
		return StagePayload
	}
}

func parseBlock(raw json.RawMessage) (Observation, error) {
	var versions map[string]json.RawMessage
	if err := json.Unmarshal(raw, &versions); err != nil {
		return Observation{}, parseError(StageBlock, ReasonWrongType, "decode version wrapper: %w", err)
	}
	if len(versions) != 1 {
		return Observation{}, parseError(StageBlock, ReasonInvalidValue, "expected exactly one version wrapper, got %d", len(versions))
	}

	var tag string
	var contentRaw json.RawMessage
	for tag, contentRaw = range versions {
	}
	if tag == "" || isNull(contentRaw) {
		return Observation{}, parseError(StageBlock, ReasonInvalidValue, "empty version wrapper")
	}

	var content struct {
		Header json.RawMessage `json:"header"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		return Observation{}, parseError(StageBlock, ReasonWrongType, "decode block content: %w", err)
	}
	if len(content.Header) == 0 {
		return Observation{}, parseError(StageHeader, ReasonMissing, "missing header wrapper")
	}
	if len(content.Body) == 0 {
		return Observation{}, parseError(StageTransactions, ReasonMissing, "missing block body")
	}

	var headerWrapper struct {
		Header json.RawMessage `json:"header"`
	}
	if err := json.Unmarshal(content.Header, &headerWrapper); err != nil {
		return Observation{}, parseError(StageHeader, ReasonWrongType, "decode header wrapper: %w", err)
	}
	if len(headerWrapper.Header) == 0 {
		return Observation{}, parseError(StageHeader, ReasonMissing, "missing inner header")
	}

	var header struct {
		Number        json.RawMessage `json:"number"`
		GasLimit      json.RawMessage `json:"gasLimit"`
		GasUsed       json.RawMessage `json:"gasUsed"`
		BaseFeePerGas json.RawMessage `json:"baseFeePerGas"`
	}
	if err := json.Unmarshal(headerWrapper.Header, &header); err != nil {
		return Observation{}, parseError(StageHeader, ReasonWrongType, "decode inner header: %w", err)
	}
	heightQuantity, err := parseHexU256(header.Number, StageHeader, "number")
	if err != nil {
		return Observation{}, err
	}
	if !heightQuantity.IsInt64() || heightQuantity.Sign() < 0 {
		return Observation{}, parseError(StageHeader, ReasonOutOfRange, "block number does not fit int64")
	}
	gasLimit, err := parseHexU256(header.GasLimit, StageHeader, "gasLimit")
	if err != nil {
		return Observation{}, err
	}
	gasUsed, err := parseHexU256(header.GasUsed, StageHeader, "gasUsed")
	if err != nil {
		return Observation{}, err
	}
	if gasLimit.Sign() > 0 && gasUsed.Cmp(gasLimit) > 0 {
		return Observation{}, parseError(StageHeader, ReasonInvalidValue, "gasUsed exceeds gasLimit")
	}
	baseFee, err := parseHexU256(header.BaseFeePerGas, StageHeader, "baseFeePerGas")
	if err != nil {
		return Observation{}, err
	}

	var body struct {
		Transactions json.RawMessage `json:"transactions"`
	}
	if err := json.Unmarshal(content.Body, &body); err != nil {
		return Observation{}, parseError(StageTransactions, ReasonWrongType, "decode block body: %w", err)
	}
	txRaws, err := requiredRawArray(body.Transactions, StageTransactions)
	if err != nil {
		return Observation{}, err
	}
	txs := make([]Transaction, 0, len(txRaws))
	var maxPriority *big.Int
	hasPriority := false
	for i, txRaw := range txRaws {
		tx, err := parseTransaction(txRaw, baseFee)
		if err != nil {
			return Observation{}, fmt.Errorf("transaction %d: %w", i, err)
		}
		txs = append(txs, tx)
		if tx.HasPriorityFee && (!hasPriority || tx.PriorityFeeWei.Cmp(maxPriority) > 0) {
			maxPriority = new(big.Int).Set(tx.PriorityFeeWei)
			hasPriority = true
		}
	}
	if !hasPriority {
		maxPriority = new(big.Int)
	}

	return Observation{
		Height:            heightQuantity.Int64(),
		WrapperTag:        tag,
		GasLimit:          gasLimit,
		GasUsed:           gasUsed,
		BaseFee:           baseFee,
		Transactions:      txs,
		TransactionCount:  len(txs),
		MaxPriorityFeeWei: maxPriority,
		HasPriorityFee:    hasPriority,
	}, nil
}

func parseTransaction(raw json.RawMessage, baseFee *big.Int) (Transaction, error) {
	var envelope struct {
		Transaction json.RawMessage `json:"transaction"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Transaction{}, parseError(StageTransactions, ReasonWrongType, "decode transaction envelope: %w", err)
	}
	if len(envelope.Transaction) == 0 || isNull(envelope.Transaction) {
		return Transaction{}, parseError(StageTransactions, ReasonMissing, "missing transaction variant")
	}

	var variants map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Transaction, &variants); err != nil {
		return Transaction{}, parseError(StageTransactions, ReasonWrongType, "decode transaction variant: %w", err)
	}
	if len(variants) != 1 {
		return Transaction{}, parseError(StageTransactions, ReasonInvalidValue, "expected exactly one transaction variant, got %d", len(variants))
	}

	var rawType string
	var dataRaw json.RawMessage
	for rawType, dataRaw = range variants {
	}
	if rawType == "" {
		return Transaction{}, parseError(StageTransactions, ReasonInvalidValue, "transaction variant key is empty")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(dataRaw, &data); err != nil || data == nil {
		return Transaction{}, parseError(StageTransactions, ReasonWrongType, "decode %s transaction data", rawType)
	}

	shape, recipient := classifyDestination(data["to"])
	normalizedType := normalizeTxType(rawType)
	tx := Transaction{
		Type:      normalizedType,
		Shape:     shape,
		Recipient: recipient,
	}

	var tip *big.Int
	var err error
	switch normalizedType {
	case TxTypeEIP1559:
		maxPriority, parseErr := parseHexU256(data["maxPriorityFeePerGas"], StageTransactions, "maxPriorityFeePerGas")
		if parseErr != nil {
			return Transaction{}, parseErr
		}
		maxFee, parseErr := parseHexU256(data["maxFeePerGas"], StageTransactions, "maxFeePerGas")
		if parseErr != nil {
			return Transaction{}, parseErr
		}
		headroom := nonnegativeSub(maxFee, baseFee)
		tip = minBig(maxPriority, headroom)
	case TxTypeLegacy, TxTypeEIP2930:
		gasPrice, parseErr := parseHexU256(data["gasPrice"], StageTransactions, "gasPrice")
		if parseErr != nil {
			return Transaction{}, parseErr
		}
		tip = nonnegativeSub(gasPrice, baseFee)
	case TxTypeOther:
		// An unknown transaction variant is admitted under a bounded type
		// label, but its fee fields are not guessed.
		return tx, nil
	default:
		err = parseError(StageTransactions, ReasonInvalidValue, "unreachable transaction type %q", normalizedType)
	}
	if err != nil {
		return Transaction{}, err
	}
	tx.PriorityFeeWei = tip
	tx.HasPriorityFee = true
	return tx, nil
}

func normalizeTxType(value string) string {
	switch value {
	case TxTypeEIP1559, TxTypeLegacy, TxTypeEIP2930:
		return value
	default:
		return TxTypeOther
	}
}

func classifyDestination(raw json.RawMessage) (shape, recipient string) {
	if len(raw) == 0 {
		return TxShapeUnknown, ""
	}
	if isNull(raw) {
		return TxShapeCreate, ""
	}
	var address string
	if err := json.Unmarshal(raw, &address); err != nil {
		return TxShapeUnknown, ""
	}
	address = strings.ToLower(address)
	if !isCanonicalAddress(address) {
		return TxShapeUnknown, ""
	}
	return TxShapeMessage, address
}

func isCanonicalAddress(address string) bool {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return false
	}
	for _, c := range address[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

type receiptObservation struct {
	count               int
	outcomes            OutcomeCounts
	hasEffectivePrice   bool
	effectivePriceCount int
	maxPriorityFeeWei   *big.Int
}

type receiptLite struct {
	Success                json.RawMessage `json:"success"`
	EffectiveGasPrice      json.RawMessage `json:"effectiveGasPrice"`
	EffectiveGasPriceSnake json.RawMessage `json:"effective_gas_price"`
}

func (r *receiptLite) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("receipt must be an object")
	}
	type receiptAlias receiptLite
	return json.Unmarshal(trimmed, (*receiptAlias)(r))
}

func parseReceipts(raw json.RawMessage, baseFee *big.Int) (receiptObservation, error) {
	if len(raw) == 0 {
		return receiptObservation{}, parseError(StageReceipts, ReasonMissing, "missing receipts")
	}
	if isNull(raw) {
		return receiptObservation{}, parseError(StageReceipts, ReasonWrongType, "receipts is null")
	}
	var receipts []receiptLite
	if err := json.Unmarshal(raw, &receipts); err != nil {
		return receiptObservation{}, parseError(StageReceipts, ReasonWrongType, "decode receipts array: %w", err)
	}
	result := receiptObservation{count: len(receipts), maxPriorityFeeWei: new(big.Int)}
	for i, receipt := range receipts {
		switch decodeOptionalBool(receipt.Success) {
		case boolTrue:
			result.outcomes.Success++
		case boolFalse:
			result.outcomes.Failed++
		default:
			result.outcomes.Unknown++
		}

		effectiveRaw := receipt.EffectiveGasPrice
		if len(effectiveRaw) == 0 {
			effectiveRaw = receipt.EffectiveGasPriceSnake
		}
		if len(effectiveRaw) == 0 || isNull(effectiveRaw) {
			continue
		}
		effective, err := parseStringQuantity(effectiveRaw, StageReceipts, "effectiveGasPrice")
		if err != nil {
			return receiptObservation{}, fmt.Errorf("receipt %d: %w", i, err)
		}
		tip := nonnegativeSub(effective, baseFee)
		result.effectivePriceCount++
		if !result.hasEffectivePrice || tip.Cmp(result.maxPriorityFeeWei) > 0 {
			result.maxPriorityFeeWei.Set(tip)
		}
		result.hasEffectivePrice = true
	}
	return result, nil
}

type optionalBool uint8

const (
	boolUnknown optionalBool = iota
	boolFalse
	boolTrue
)

func decodeOptionalBool(raw json.RawMessage) optionalBool {
	if len(raw) == 0 || isNull(raw) {
		return boolUnknown
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return boolUnknown
	}
	if value {
		return boolTrue
	}
	return boolFalse
}

func parsePrecompileCalls(raw json.RawMessage) (PrecompileOutcomeCounts, error) {
	groups, err := requiredRawArray(raw, StagePrecompileCalls)
	if err != nil {
		return PrecompileOutcomeCounts{}, err
	}
	var result PrecompileOutcomeCounts
	for groupIndex, groupRaw := range groups {
		var group []json.RawMessage
		if err := json.Unmarshal(groupRaw, &group); err != nil || len(group) != 2 {
			return PrecompileOutcomeCounts{}, parseError(StagePrecompileCalls, ReasonWrongType, "group %d must be [address,calls]", groupIndex)
		}
		var ignoredAddress string
		if isNull(group[0]) || json.Unmarshal(group[0], &ignoredAddress) != nil || ignoredAddress == "" {
			return PrecompileOutcomeCounts{}, parseError(StagePrecompileCalls, ReasonWrongType, "group %d address is not a nonempty string", groupIndex)
		}
		calls, err := requiredRawArray(group[1], StagePrecompileCalls)
		if err != nil {
			return PrecompileOutcomeCounts{}, fmt.Errorf("group %d: %w", groupIndex, err)
		}
		for callIndex, callRaw := range calls {
			var call []json.RawMessage
			if err := json.Unmarshal(callRaw, &call); err != nil || len(call) != 2 {
				return PrecompileOutcomeCounts{}, parseError(StagePrecompileCalls, ReasonWrongType, "group %d call %d must be [request,result]", groupIndex, callIndex)
			}
			var request map[string]json.RawMessage
			if err := json.Unmarshal(call[0], &request); err != nil || request == nil {
				return PrecompileOutcomeCounts{}, parseError(StagePrecompileCalls, ReasonWrongType, "group %d call %d request is not an object", groupIndex, callIndex)
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(call[1], &response); err == nil && response != nil {
				if okRaw, ok := response["Ok"]; ok && !isNull(okRaw) {
					result.OK++
					continue
				}
			}
			result.Other++
		}
	}
	return result, nil
}

func requiredRawArray(raw json.RawMessage, stage ParseStage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, parseError(stage, ReasonMissing, "missing array")
	}
	if isNull(raw) {
		return nil, parseError(stage, ReasonWrongType, "array is null")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, parseError(stage, ReasonWrongType, "decode array: %w", err)
	}
	return values, nil
}

func parseHexU256(raw json.RawMessage, stage ParseStage, field string) (*big.Int, error) {
	if len(raw) == 0 {
		return nil, parseError(stage, ReasonMissing, "missing %s", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, parseError(stage, ReasonWrongType, "%s is not a string", field)
	}
	if !strings.HasPrefix(value, "0x") || len(value) == 2 {
		return nil, parseError(stage, ReasonInvalidValue, "%s is not a nonempty hex quantity", field)
	}
	parsed, ok := new(big.Int).SetString(value[2:], 16)
	if !ok || parsed.Sign() < 0 {
		return nil, parseError(stage, ReasonInvalidValue, "invalid %s quantity", field)
	}
	if parsed.BitLen() > 256 {
		return nil, parseError(stage, ReasonOutOfRange, "%s exceeds 256 bits", field)
	}
	return parsed, nil
}

func parseStringQuantity(raw json.RawMessage, stage ParseStage, field string) (*big.Int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, parseError(stage, ReasonMissing, "missing %s", field)
	}
	var value string
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, parseError(stage, ReasonWrongType, "%s is not a quantity string", field)
		}
	} else {
		// json.RawMessage preserves decimal integer bytes, avoiding float64
		// conversion before exact fee arithmetic.
		value = string(trimmed)
	}
	base := 10
	digits := value
	if strings.HasPrefix(value, "0x") {
		base = 16
		digits = value[2:]
	}
	if digits == "" {
		return nil, parseError(stage, ReasonInvalidValue, "%s is empty", field)
	}
	parsed, ok := new(big.Int).SetString(digits, base)
	if !ok || parsed.Sign() < 0 {
		return nil, parseError(stage, ReasonInvalidValue, "invalid %s quantity", field)
	}
	if parsed.BitLen() > 256 {
		return nil, parseError(stage, ReasonOutOfRange, "%s exceeds 256 bits", field)
	}
	return parsed, nil
}

func nonnegativeSub(a, b *big.Int) *big.Int {
	if a.Cmp(b) <= 0 {
		return new(big.Int)
	}
	return new(big.Int).Sub(a, b)
}

func minBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) <= 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// WeiToGwei performs the unit conversion as an exact rational and only rounds
// at the final float64 metric boundary.
func WeiToGwei(wei *big.Int) (float64, error) {
	if wei == nil || wei.Sign() < 0 {
		return 0, fmt.Errorf("wei must be a nonnegative integer")
	}
	value, _ := new(big.Rat).SetFrac(new(big.Int).Set(wei), big.NewInt(1_000_000_000)).Float64()
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("gwei value is not representable")
	}
	return value, nil
}

func GasValues(gasUsed, gasLimit *big.Int) (used, limit float64, ratio *float64, err error) {
	if gasUsed == nil || gasLimit == nil || gasUsed.Sign() < 0 || gasLimit.Sign() < 0 {
		return 0, 0, nil, fmt.Errorf("gas values must be nonnegative integers")
	}
	used, _ = new(big.Float).SetInt(gasUsed).Float64()
	limit, _ = new(big.Float).SetInt(gasLimit).Float64()
	if math.IsInf(used, 0) || math.IsInf(limit, 0) {
		return 0, 0, nil, fmt.Errorf("gas value is not representable")
	}
	if gasLimit.Sign() == 0 {
		return used, limit, nil, nil
	}
	if gasUsed.Cmp(gasLimit) > 0 {
		return 0, 0, nil, fmt.Errorf("gas used exceeds gas limit")
	}
	r, _ := new(big.Rat).SetFrac(new(big.Int).Set(gasUsed), new(big.Int).Set(gasLimit)).Float64()
	if r < 0 || r > 1 || math.IsNaN(r) || math.IsInf(r, 0) {
		return 0, 0, nil, fmt.Errorf("gas ratio is outside [0,1]")
	}
	return used, limit, &r, nil
}
