package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	if err := json.Unmarshal(outer[0], &tsStr); err != nil {
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
	if len(payload.SignedActions) == 0 || string(payload.SignedActions) == "null" {
		return mempoolTxParsedLine{}, "missing_signed_actions", false
	}
	var signedActions []mempoolTxSignedAction
	if err := json.Unmarshal(payload.SignedActions, &signedActions); err != nil {
		return mempoolTxParsedLine{}, "invalid_signed_actions", false
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
	switch {
	case len(action.Orders) > 0:
		return len(action.Orders)
	case len(action.Cancels) > 0:
		return len(action.Cancels)
	case len(action.Modifies) > 0:
		return len(action.Modifies)
	case action.Order != nil:
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
