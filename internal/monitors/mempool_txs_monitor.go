package monitors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/actiontypes"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var mempoolTxOrderTIFAllowlist = map[string]bool{
	"Alo":            true,
	"FrontendMarket": true,
	"Gtc":            true,
	"Ioc":            true,
	"unknown":        true,
}

type mempoolTxPayload struct {
	SignedActions json.RawMessage `json:"signed_actions"`
}

type mempoolTxSignedAction struct {
	Action mempoolTxAction `json:"action"`
}

type mempoolTxSignedActionEnvelope struct {
	Action json.RawMessage `json:"action"`
}

type mempoolTxAction struct {
	Type     string            `json:"type"`
	Orders   []mempoolTxOrder  `json:"orders,omitempty"`
	Cancels  []json.RawMessage `json:"cancels,omitempty"`
	Modifies []mempoolTxModify `json:"modifies,omitempty"`
	Order    *mempoolTxOrder   `json:"order,omitempty"`
}

type mempoolTxModify struct {
	Order *mempoolTxOrder `json:"order,omitempty"`
}

type rawMempoolTxAction struct {
	Type     json.RawMessage `json:"type"`
	Orders   json.RawMessage `json:"orders"`
	Cancels  json.RawMessage `json:"cancels"`
	Modifies json.RawMessage `json:"modifies"`
	Order    json.RawMessage `json:"order"`
}

func (a *mempoolTxAction) UnmarshalJSON(data []byte) error {
	var raw rawMempoolTxAction
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var actionType string
	if err := unmarshalRequiredJSON(raw.Type, &actionType); err != nil || strings.TrimSpace(actionType) == "" {
		return errors.New("action type must be a non-empty string")
	}
	*a = mempoolTxAction{Type: actionType}

	var err error
	switch actionType {
	case "order":
		a.Orders, err = decodeMempoolTxOrders(raw.Orders)
	case "cancel", "cancelByCloid":
		a.Cancels, err = decodeMempoolTxCancels(raw.Cancels)
	case "batchModify":
		a.Modifies, err = decodeMempoolTxModifies(raw.Modifies)
	case "modify":
		var order mempoolTxOrder
		if !rawJSONObject(raw.Order) {
			return errors.New("modify order must be an object")
		}
		if err := json.Unmarshal(raw.Order, &order); err != nil {
			return err
		}
		a.Order = &order
	default:
		// Scalar and future action types count as one operation. Fields owned by
		// another action shape are deliberately ignored and cannot skew counts or
		// emit order labels.
	}
	return err
}

func decodeMempoolTxOrders(raw json.RawMessage) ([]mempoolTxOrder, error) {
	if !rawJSONArray(raw) {
		return nil, errors.New("orders must be an array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	orders := make([]mempoolTxOrder, 0, len(items))
	for _, item := range items {
		if !rawJSONObject(item) {
			return nil, errors.New("order must be an object")
		}
		var order mempoolTxOrder
		if err := json.Unmarshal(item, &order); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func decodeMempoolTxCancels(raw json.RawMessage) ([]json.RawMessage, error) {
	if !rawJSONArray(raw) {
		return nil, errors.New("cancels must be an array")
	}
	var cancels []json.RawMessage
	if err := json.Unmarshal(raw, &cancels); err != nil {
		return nil, err
	}
	for _, cancel := range cancels {
		if !rawJSONObject(cancel) {
			return nil, errors.New("cancel must be an object")
		}
	}
	return cancels, nil
}

func decodeMempoolTxModifies(raw json.RawMessage) ([]mempoolTxModify, error) {
	if !rawJSONArray(raw) {
		return nil, errors.New("modifies must be an array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	modifies := make([]mempoolTxModify, 0, len(items))
	for _, item := range items {
		if !rawJSONObject(item) {
			return nil, errors.New("modify must be an object")
		}
		var rawModify struct {
			Order json.RawMessage `json:"order"`
		}
		if err := json.Unmarshal(item, &rawModify); err != nil || !rawJSONObject(rawModify.Order) {
			return nil, errors.New("batch modify order must be an object")
		}
		var order mempoolTxOrder
		if err := json.Unmarshal(rawModify.Order, &order); err != nil {
			return nil, err
		}
		modifies = append(modifies, mempoolTxModify{Order: &order})
	}
	return modifies, nil
}

type mempoolTxOrder struct {
	IsBuy *bool `json:"b,omitempty"`
	T     struct {
		Limit *struct {
			TIF json.RawMessage `json:"tif,omitempty"`
		} `json:"limit,omitempty"`
		Trigger json.RawMessage `json:"trigger,omitempty"`
	} `json:"t,omitempty"`
}

type mempoolTxOrderLabel struct {
	side string
	tif  string
}

type mempoolTxParsedLine struct {
	timestamp       time.Time
	signedActions   int
	operations      int
	actionCounts    map[string]int
	operationCounts map[string]int
	orderCounts     map[mempoolTxOrderLabel]int
	parserEvents    map[string]int
}

// StartMempoolTxsMonitor watches $NODE_HOME/data/mempool_txs/hourly.
//
// This is the split-client-blocks stream documented by Hyperliquid: a node
// with split_client_blocks enabled receives uncommitted mempool transactions
// and writes records of the form ["<iso>", {"tx_hash": "...",
// "signed_actions": [...]}]. Files are large on mainnet, so we seek to EOF on
// exporter startup, then offset-track appended complete JSONL records.
func StartMempoolTxsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "mempool_txs", "hourly")
	metrics.RegisterSource(metrics.SourceMempoolTxs, true)
	logger.InfoComponent("mempool_txs", "watching %s", root)
	var lastReceipt time.Time
	tailStream(ctx, tailStreamOpts{
		component:   "mempool_txs",
		name:        "mempool_txs stream",
		rescanEvery: 2 * time.Second,
		eofSleep:    250 * time.Millisecond,
		bufSize:     1 << 20,
		resolve: func() (string, error) {
			metrics.MarkMonitorAttempt("mempool_txs")
			metrics.MarkSourceAttempt(metrics.SourceMempoolTxs)
			path, err := latestHourlyFile(root)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					metrics.MarkSourceAbsent(metrics.SourceMempoolTxs)
				} else {
					metrics.MarkSourceError(metrics.SourceMempoolTxs, metrics.SourceFailureDiscovery)
				}
			}
			return path, err
		},
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("mempool_txs")
			metrics.MarkSourceAttempt(metrics.SourceMempoolTxs)
			metrics.HLMempoolTxsBytesTotal.Add(float64(len(line)))
			stats, reason, ok := parseMempoolTxsLineDetailed([]byte(line))
			if !ok {
				metrics.HLMempoolTxsParserEventsTotal.WithLabelValues(reason).Inc()
				metrics.MarkSourceError(metrics.SourceMempoolTxs, metrics.SourceFailureSchema)
				return
			}
			publishMempoolTxsLine(stats)
			lastReceipt = time.Now()
			metrics.HLMempoolTxsSampleAgeSeconds.WithLabelValues().Set(0)
			metrics.MarkSourceValidObservation(metrics.SourceMempoolTxs, stats.timestamp)
			metrics.MarkSourcePublication(metrics.SourceMempoolTxs)
			metrics.MarkMonitorValidObservation("mempool_txs")
			metrics.MarkMonitorPublication("mempool_txs")
		},
		onIdle: func() {
			if !lastReceipt.IsZero() {
				metrics.HLMempoolTxsSampleAgeSeconds.WithLabelValues().Set(time.Since(lastReceipt).Seconds())
			}
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(metrics.SourceMempoolTxs, true)
		},
		onFailure: func(failure tailStreamFailure) {
			markTailSourceFailure(metrics.SourceMempoolTxs, failure)
		},
	})
}

func readMempoolTxsEvents(path string, offset int64) (int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, 0, err
	}

	reader := bufio.NewReaderSize(f, 1<<20)
	processed := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if err == io.EOF && line[len(line)-1] != '\n' {
				break
			}
			offset += int64(len(line))
			metrics.HLMempoolTxsBytesTotal.Add(float64(len(line)))
			if stats, reason, ok := parseMempoolTxsLineDetailed(line); ok {
				publishMempoolTxsLine(stats)
				processed++
			} else {
				metrics.HLMempoolTxsParserEventsTotal.WithLabelValues(reason).Inc()
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return offset, processed, err
		}
	}
	return offset, processed, nil
}

func parseMempoolTxsLine(line []byte) (mempoolTxParsedLine, bool) {
	stats, _, ok := parseMempoolTxsLineDetailed(line)
	return stats, ok
}

func parseMempoolTxsLineDetailed(line []byte) (mempoolTxParsedLine, string, bool) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil || len(outer) != 2 {
		return mempoolTxParsedLine{}, "invalid_envelope", false
	}
	var tsStr string
	if err := unmarshalRequiredJSON(outer[0], &tsStr); err != nil {
		return mempoolTxParsedLine{}, "invalid_timestamp", false
	}
	ts, ok := parseVisorTime(tsStr)
	if !ok {
		return mempoolTxParsedLine{}, "invalid_timestamp", false
	}

	var payload mempoolTxPayload
	if err := json.Unmarshal(outer[1], &payload); err != nil {
		return mempoolTxParsedLine{}, "invalid_payload", false
	}
	if len(payload.SignedActions) == 0 || bytes.Equal(bytes.TrimSpace(payload.SignedActions), []byte("null")) {
		return mempoolTxParsedLine{}, "missing_signed_actions", false
	}
	if !rawJSONArray(payload.SignedActions) {
		return mempoolTxParsedLine{}, "invalid_signed_actions", false
	}
	var rawSignedActions []json.RawMessage
	if err := json.Unmarshal(payload.SignedActions, &rawSignedActions); err != nil {
		return mempoolTxParsedLine{}, "invalid_signed_actions", false
	}
	if len(rawSignedActions) == 0 {
		return mempoolTxParsedLine{}, "invalid_signed_actions", false
	}
	signedActions := make([]mempoolTxSignedAction, 0, len(rawSignedActions))
	for _, raw := range rawSignedActions {
		if !rawJSONObject(raw) {
			return mempoolTxParsedLine{}, "invalid_signed_action", false
		}
		var envelope mempoolTxSignedActionEnvelope
		if err := unmarshalRequiredJSON(raw, &envelope); err != nil || !rawJSONObject(envelope.Action) {
			return mempoolTxParsedLine{}, "invalid_signed_action", false
		}
		var action mempoolTxAction
		if err := unmarshalRequiredJSON(envelope.Action, &action); err != nil {
			return mempoolTxParsedLine{}, "invalid_signed_action", false
		}
		signedActions = append(signedActions, mempoolTxSignedAction{Action: action})
	}

	stats := mempoolTxParsedLine{
		timestamp:       ts,
		signedActions:   len(signedActions),
		actionCounts:    make(map[string]int),
		operationCounts: make(map[string]int),
		orderCounts:     make(map[mempoolTxOrderLabel]int),
		parserEvents:    make(map[string]int),
	}

	for _, signedAction := range signedActions {
		actionType, known := mempoolTxActionTypeLabel(signedAction.Action.Type)
		if !known {
			stats.parserEvents["unknown_action"]++
		}
		ops := mempoolTxOperationCount(signedAction.Action)
		stats.operations += ops
		stats.actionCounts[actionType]++
		stats.operationCounts[actionType] += ops

		for _, order := range signedAction.Action.Orders {
			label, reason := mempoolTxOrderMetricLabel(order)
			stats.orderCounts[label]++
			if reason != "" {
				stats.parserEvents[reason]++
			}
		}
		if signedAction.Action.Order != nil {
			label, reason := mempoolTxOrderMetricLabel(*signedAction.Action.Order)
			stats.orderCounts[label]++
			if reason != "" {
				stats.parserEvents[reason]++
			}
		}
		for _, modify := range signedAction.Action.Modifies {
			if modify.Order != nil {
				label, reason := mempoolTxOrderMetricLabel(*modify.Order)
				stats.orderCounts[label]++
				if reason != "" {
					stats.parserEvents[reason]++
				}
			}
		}
	}

	return stats, "", true
}

func publishMempoolTxsLine(stats mempoolTxParsedLine) {
	metrics.HLMempoolTxsSeenTotal.Inc()
	metrics.HLMempoolTxsSignedActionsPerRecord.Observe(float64(stats.signedActions))
	metrics.HLMempoolTxsOperationsPerRecord.Observe(float64(stats.operations))

	for actionType, count := range stats.actionCounts {
		metrics.HLMempoolTxsSignedActionsTotal.WithLabelValues(actionType).Add(float64(count))
	}
	for actionType, count := range stats.operationCounts {
		metrics.HLMempoolTxsOperationsTotal.WithLabelValues(actionType).Add(float64(count))
	}
	for label, count := range stats.orderCounts {
		metrics.HLMempoolTxsOrderOperationsTotal.WithLabelValues(label.side, label.tif).Add(float64(count))
	}
	for reason, count := range stats.parserEvents {
		metrics.HLMempoolTxsParserEventsTotal.WithLabelValues(reason).Add(float64(count))
	}

	if !stats.timestamp.IsZero() {
		metrics.HLMempoolTxsLatestTime.WithLabelValues().Set(float64(stats.timestamp.Unix()))
	}
}

func mempoolTxActionTypeLabel(actionType string) (string, bool) {
	return actiontypes.Normalize(actionType)
}

func mempoolTxOperationCount(action mempoolTxAction) int {
	switch action.Type {
	case "order":
		return len(action.Orders)
	case "cancel", "cancelByCloid":
		return len(action.Cancels)
	case "batchModify":
		return len(action.Modifies)
	case "modify":
		return 1
	default:
		return 1
	}
}

func mempoolTxOrderMetricLabel(order mempoolTxOrder) (mempoolTxOrderLabel, string) {
	side := "unknown"
	if order.IsBuy != nil {
		if *order.IsBuy {
			side = "buy"
		} else {
			side = "sell"
		}
	}

	if len(order.T.Trigger) > 0 && string(order.T.Trigger) != "null" {
		var trigger map[string]json.RawMessage
		if err := json.Unmarshal(order.T.Trigger, &trigger); err != nil {
			return mempoolTxOrderLabel{side: side, tif: "unknown"}, "invalid_trigger"
		}
		return mempoolTxOrderLabel{side: side, tif: "trigger"}, ""
	}
	if order.T.Limit == nil {
		return mempoolTxOrderLabel{side: side, tif: "unknown"}, "missing_order_kind"
	}
	if len(order.T.Limit.TIF) == 0 {
		return mempoolTxOrderLabel{side: side, tif: "unknown"}, "missing_tif"
	}
	if string(order.T.Limit.TIF) == "null" {
		return mempoolTxOrderLabel{side: side, tif: "unknown"}, "null_tif"
	}
	var tif string
	if err := json.Unmarshal(order.T.Limit.TIF, &tif); err != nil {
		return mempoolTxOrderLabel{side: side, tif: "unknown"}, "invalid_tif_type"
	}
	if !mempoolTxOrderTIFAllowlist[tif] {
		return mempoolTxOrderLabel{side: side, tif: "other"}, "unknown_tif"
	}
	return mempoolTxOrderLabel{side: side, tif: tif}, ""
}
