package replica

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/actiontypes"
)

type Parser struct {
	bufferSize int
	blockPool  sync.Pool
}

// ParseError carries a closed parser stage/reason pair suitable for bounded
// telemetry. Error text and payload values are deliberately excluded from the
// metric classification and remain log-only context.
type ParseError struct {
	Stage  string
	Reason string
	Err    error
}

func (e *ParseError) Error() string {
	if e.Err == nil {
		return e.Stage + ": " + e.Reason
	}
	return e.Stage + ": " + e.Reason + ": " + e.Err.Error()
}

func (e *ParseError) Unwrap() error { return e.Err }

func parseError(stage, reason string, err error) error {
	return &ParseError{Stage: stage, Reason: reason, Err: err}
}

// ClassifyError returns only fixed values. Callers must never derive metric
// labels from err.Error().
func ClassifyError(err error) (stage, reason string) {
	var target *ParseError
	if errors.As(err, &target) {
		return target.Stage, target.Reason
	}
	return "record", "other"
}

func NewParser(bufferSizeMB int) *Parser {
	p := &Parser{bufferSize: bufferSizeMB * 1024 * 1024}
	p.blockPool = sync.Pool{New: func() interface{} { return &ReplicaBlock{} }}
	return p
}

func (p *Parser) getBlock() *ReplicaBlock {
	block := p.blockPool.Get().(*ReplicaBlock)
	block.Reset()
	return block
}

// ExtractMetrics validates the full block/action-bundle record before
// returning anything publishable. A malformed item in the middle rejects the
// complete block instead of leaking a partial counter update. An explicit
// empty bundle array remains a valid empty block.
func (p *Parser) ExtractMetrics(block *ReplicaBlock) (*BlockMetrics, error) {
	start := time.Now()
	if block == nil {
		return nil, parseError("block", "nil", nil)
	}
	if block.Height <= 0 {
		return nil, parseError("block", "invalid_height", nil)
	}
	if block.ABCIBlock.Round <= 0 {
		return nil, parseError("block", "invalid_round", nil)
	}
	if block.ABCIBlock.Proposer == "" {
		return nil, parseError("block", "missing_proposer", nil)
	}

	blockTime, err := parseBlockTime(block.ABCIBlock.Time)
	if err != nil {
		return nil, parseError("block", "invalid_time", err)
	}
	if len(block.ABCIBlock.SignedActionBundles) == 0 || bytes.Equal(bytes.TrimSpace(block.ABCIBlock.SignedActionBundles), []byte("null")) {
		return nil, parseError("bundles", "missing", nil)
	}

	actionCounts := make(map[string]int)
	operationCounts := make(map[string]int)
	multiSigInner := make(map[string]int)
	parserEvents := make(map[string]int)
	totalActions, totalOperations := 0, 0
	totalBundles, err := p.parseActionBundles(
		block.ABCIBlock.SignedActionBundles,
		actionCounts,
		operationCounts,
		multiSigInner,
		parserEvents,
		&totalActions,
		&totalOperations,
	)
	if err != nil {
		return nil, err
	}

	responses := parseReplicaResponses(block.Resps, totalActions)
	return &BlockMetrics{
		Height:          block.Height,
		Round:           block.ABCIBlock.Round,
		ParentRound:     block.ABCIBlock.ParentRound,
		HardforkVersion: block.ABCIBlock.Hardfork.Version,
		Time:            blockTime,
		Proposer:        block.ABCIBlock.Proposer,
		TotalBundles:    totalBundles,
		TotalActions:    totalActions,
		ActionCounts:    actionCounts,
		TotalOperations: totalOperations,
		OperationCounts: operationCounts,
		MultiSigInner:   multiSigInner,
		ParserEvents:    parserEvents,
		Responses:       responses,
		ProcessDuration: time.Since(start),
	}, nil
}

func parseBlockTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("missing block time")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.999999999", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time: %w", err)
	}
	return parsed.UTC(), nil
}

func (p *Parser) parseActionBundles(
	bundlesJSON json.RawMessage,
	actionCounts, operationCounts, multiSigInner, parserEvents map[string]int,
	totalActions, totalOperations *int,
) (int, error) {
	var bundles []json.RawMessage
	if err := json.Unmarshal(bundlesJSON, &bundles); err != nil {
		return 0, parseError("bundles", "invalid_array", err)
	}

	for bundleIndex, bundlePair := range bundles {
		var pair []json.RawMessage
		if err := json.Unmarshal(bundlePair, &pair); err != nil || len(pair) != 2 {
			return 0, parseError("bundle", "invalid_pair", fmt.Errorf("bundle %d", bundleIndex))
		}
		var bundleData struct {
			SignedActions json.RawMessage `json:"signed_actions"`
		}
		if err := json.Unmarshal(pair[1], &bundleData); err != nil {
			return 0, parseError("bundle", "invalid_data", fmt.Errorf("bundle %d: %w", bundleIndex, err))
		}
		if len(bundleData.SignedActions) == 0 || bytes.Equal(bytes.TrimSpace(bundleData.SignedActions), []byte("null")) {
			return 0, parseError("bundle", "missing_signed_actions", fmt.Errorf("bundle %d", bundleIndex))
		}
		var signedActions []struct {
			Action struct {
				Type     string          `json:"type"`
				Orders   json.RawMessage `json:"orders,omitempty"`
				Cancels  json.RawMessage `json:"cancels,omitempty"`
				Modifies json.RawMessage `json:"modifies,omitempty"`
				Payload  json.RawMessage `json:"payload,omitempty"`
			} `json:"action"`
		}
		if err := json.Unmarshal(bundleData.SignedActions, &signedActions); err != nil {
			return 0, parseError("bundle", "invalid_signed_actions", fmt.Errorf("bundle %d: %w", bundleIndex, err))
		}
		if len(signedActions) == 0 {
			return 0, parseError("bundle", "zero_signed_actions", fmt.Errorf("bundle %d", bundleIndex))
		}

		for actionIndex, signed := range signedActions {
			rawType := signed.Action.Type
			if rawType == "" {
				return 0, parseError("action", "missing_type", fmt.Errorf("bundle %d action %d", bundleIndex, actionIndex))
			}
			actionType, known := actiontypes.Normalize(rawType)
			if !known {
				parserEvents["unknown_action"]++
			}
			operationCount, err := replicaOperationCount(actionType, signed.Action.Orders, signed.Action.Cancels, signed.Action.Modifies)
			if err != nil {
				return 0, fmt.Errorf("bundle %d action %d: %w", bundleIndex, actionIndex, err)
			}
			actionCounts[actionType]++
			operationCounts[actionType] += operationCount
			*totalActions++
			*totalOperations += operationCount

			if actionType == "multiSig" {
				category, reason := parseMultiSigInnerCategory(signed.Action.Payload)
				multiSigInner[category]++
				if reason != "" {
					parserEvents[reason]++
				}
			}
		}
	}
	return len(bundles), nil
}

func replicaOperationCount(actionType string, orders, cancels, modifies json.RawMessage) (int, error) {
	switch actionType {
	case ActionTypeOrder:
		return countJSONArrayElementsStrict("orders", orders)
	case ActionTypeCancel, ActionTypeCancelByCloid:
		return countJSONArrayElementsStrict("cancels", cancels)
	case ActionTypeBatchModify:
		return countJSONArrayElementsStrict("modifies", modifies)
	default:
		return 1, nil
	}
}

func countJSONArrayElementsStrict(field string, data json.RawMessage) (int, error) {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return 0, parseError("operation", "missing_"+field, nil)
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(data, &elems); err != nil {
		return 0, parseError("operation", "invalid_"+field, err)
	}
	return len(elems), nil
}

// countJSONArrayElements retains the old test/helper contract while using the
// strict decoder internally. Malformed data counts as one action for callers
// that explicitly want the legacy fallback; block extraction never uses it.
func countJSONArrayElements(data json.RawMessage) int {
	count, err := countJSONArrayElementsStrict("items", data)
	if err != nil {
		return 1
	}
	return count
}

func parseMultiSigInnerCategory(raw json.RawMessage) (category, reason string) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return actiontypes.Other, "multisig_inner_missing"
	}
	var payload struct {
		Action struct {
			Type string `json:"type"`
		} `json:"action"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Action.Type == "" {
		return actiontypes.Other, "multisig_inner_missing"
	}
	inner, known := actiontypes.Normalize(payload.Action.Type)
	if !known {
		return actiontypes.Other, "unknown_multisig_inner"
	}
	return actiontypes.Category(inner), ""
}

func parseReplicaResponses(raw json.RawMessage, requestActions int) ResponseMetrics {
	out := ResponseMetrics{
		Coverage:       "unavailable",
		ActionStatuses: make(map[string]int),
		Outcomes:       make(map[string]int),
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		out.CountRelation = countRelation(requestActions, 0)
		return out
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil || len(wrapper) != 1 {
		out.Coverage = "malformed"
		out.MalformedContainers = 1
		out.CountRelation = countRelation(requestActions, 0)
		return out
	}
	full, ok := wrapper["Full"]
	if !ok {
		out.Coverage = "malformed"
		out.MalformedContainers = 1
		out.CountRelation = countRelation(requestActions, 0)
		return out
	}
	var responseBundles []json.RawMessage
	if err := json.Unmarshal(full, &responseBundles); err != nil {
		out.Coverage = "malformed"
		out.MalformedContainers = 1
		out.CountRelation = countRelation(requestActions, 0)
		return out
	}
	out.Coverage = "available"
	for _, rawBundle := range responseBundles {
		var pair []json.RawMessage
		if err := json.Unmarshal(rawBundle, &pair); err != nil || len(pair) != 2 {
			out.MalformedContainers++
			out.Coverage = "malformed"
			continue
		}
		var records []json.RawMessage
		if err := json.Unmarshal(pair[1], &records); err != nil {
			out.MalformedContainers++
			out.Coverage = "malformed"
			continue
		}
		for _, rawRecord := range records {
			out.Records++
			if !parseReplicaResponseRecord(rawRecord, &out) {
				out.MalformedRecords++
				out.Coverage = "malformed"
			}
		}
	}
	out.CountRelation = countRelation(requestActions, out.Records)
	return out
}

func parseReplicaResponseRecord(raw json.RawMessage, out *ResponseMetrics) bool {
	var record struct {
		Res json.RawMessage `json:"res"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || len(record.Res) == 0 {
		return false
	}
	var result struct {
		Status   json.RawMessage `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(record.Res, &result); err != nil || len(result.Status) == 0 {
		return false
	}
	var status string
	if err := json.Unmarshal(result.Status, &status); err != nil {
		return false
	}
	switch status {
	case "ok", "err":
		out.ActionStatuses[status]++
	default:
		out.ActionStatuses["other"]++
	}

	if len(result.Response) == 0 || bytes.Equal(bytes.TrimSpace(result.Response), []byte("null")) {
		return true
	}
	var responseObject struct {
		Data struct {
			Statuses json.RawMessage `json:"statuses"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.Response, &responseObject); err != nil {
		// String responses are valid error/default responses with no nested
		// execution-status array.
		var responseText string
		return json.Unmarshal(result.Response, &responseText) == nil
	}
	if len(responseObject.Data.Statuses) == 0 {
		return true
	}
	var statuses []json.RawMessage
	if err := json.Unmarshal(responseObject.Data.Statuses, &statuses); err != nil {
		return false
	}
	for _, rawStatus := range statuses {
		out.Outcomes[normalizeReplicaOutcome(rawStatus)]++
	}
	return true
}

func normalizeReplicaOutcome(raw json.RawMessage) string {
	var scalar string
	if err := json.Unmarshal(raw, &scalar); err == nil {
		if scalar == "success" {
			return "success"
		}
		return "other"
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return "other"
	}
	for key := range object {
		switch key {
		case "resting", "error":
			return key
		case "waitingForTrigger":
			return "waiting_for_trigger"
		default:
			return "other"
		}
	}
	return "other"
}

func countRelation(requests, responses int) string {
	switch {
	case responses < requests:
		return "fewer"
	case responses > requests:
		return "more"
	default:
		return "equal"
	}
}

func (p *Parser) ParseBlockFromLine(line []byte) (*ReplicaBlock, error) {
	block := p.getBlock()
	if err := json.Unmarshal(line, block); err != nil {
		p.blockPool.Put(block)
		return nil, parseError("record", "invalid_json", err)
	}
	return block, nil
}

func (p *Parser) ReturnBlock(block *ReplicaBlock) {
	block.Reset()
	p.blockPool.Put(block)
}
