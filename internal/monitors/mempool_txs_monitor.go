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

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const mempoolTxsPollInterval = 10 * time.Second

// mempoolTxActionTypeAllowlist bounds the action-type label set. New action
// variants fall into type="other" until reviewed.
var mempoolTxActionTypeAllowlist = map[string]bool{
	"approveAgent":                     true,
	"approveBuilderFee":                true,
	"agentEnableDexAbstraction":        true,
	"agentSendAsset":                   true,
	"agentSetAbstraction":              true,
	"batchModify":                      true,
	"cancel":                           true,
	"cancelByCloid":                    true,
	"claimRewards":                     true,
	"evmRawTx":                         true,
	"modify":                           true,
	"multiSig":                         true,
	"NetChildVaultPositionsAction":     true,
	"noop":                             true,
	"order":                            true,
	"perpDeploy":                       true,
	"scheduleCancel":                   true,
	"sendAsset":                        true,
	"setReferrer":                      true,
	"SetGlobalAction":                  true,
	"spotSend":                         true,
	"subAccountSpotTransfer":           true,
	"subAccountTransfer":               true,
	"tokenDelegate":                    true,
	"twapCancel":                       true,
	"twapOrder":                        true,
	"updateIsolatedMargin":             true,
	"updateLeverage":                   true,
	"usdClassTransfer":                 true,
	"usdSend":                          true,
	"userSetAbstraction":               true,
	"ValidatorSignWithdrawalAction":    true,
	"vaultTransfer":                    true,
	"VoteEthFinalizedWithdrawalAction": true,
	"voteAppHash":                      true,
	"withdraw3":                        true,
}

var mempoolTxOrderTIFAllowlist = map[string]bool{
	"Alo":            true,
	"FrontendMarket": true,
	"Gtc":            true,
	"Ioc":            true,
	"unknown":        true,
}

type mempoolTxOuterRecord [2]json.RawMessage

type mempoolTxPayload struct {
	SignedActions []mempoolTxSignedAction `json:"signed_actions"`
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
		Limit struct {
			TIF string `json:"tif,omitempty"`
		} `json:"limit,omitempty"`
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
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("mempool_txs",
			"mempool_txs directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("mempool_txs", "watching %s", root)

	var currentFile string
	var currentOffset int64
	var initialized bool

	tick := func() {
		latest, err := latestHourlyFile(root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.DebugComponent("mempool_txs", "find latest: %v", err)
			}
			return
		}

		if latest != currentFile {
			info, err := os.Stat(latest)
			if err != nil {
				return
			}
			currentFile = latest
			if !initialized {
				currentOffset = info.Size()
				initialized = true
				logger.InfoComponent("mempool_txs", "switched to %s (starting at EOF=%d)", latest, info.Size())
				return
			}
			currentOffset = 0
			logger.InfoComponent("mempool_txs", "switched to %s (starting at beginning)", latest)
		}

		if currentFile == "" {
			return
		}
		if info, err := os.Stat(currentFile); err == nil && info.Size() < currentOffset {
			currentOffset = 0
		}

		newOffset, _, err := readMempoolTxsEvents(currentFile, currentOffset)
		if err != nil {
			logger.DebugComponent("mempool_txs", "read %s: %v", currentFile, err)
			return
		}
		currentOffset = newOffset
		// refresh the age unconditionally: a quiet or dead stream must show
		// a climbing age, which is the whole point of the gauge
		if !mempoolTxsLastRecord.IsZero() {
			metrics.HLMempoolTxsSampleAgeSeconds.Set(time.Since(mempoolTxsLastRecord).Seconds())
		}
		metrics.MarkMonitorTick("mempool_txs")
	}

	ticker := time.NewTicker(mempoolTxsPollInterval)
	defer ticker.Stop()
	tick()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
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
			if stats, ok := parseMempoolTxsLine(line); ok {
				publishMempoolTxsLine(stats)
				processed++
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
	var outer mempoolTxOuterRecord
	if err := json.Unmarshal(line, &outer); err != nil {
		return mempoolTxParsedLine{}, false
	}
	var tsStr string
	if err := json.Unmarshal(outer[0], &tsStr); err != nil {
		return mempoolTxParsedLine{}, false
	}
	ts, ok := parseVisorTime(tsStr)
	if !ok {
		return mempoolTxParsedLine{}, false
	}

	var payload mempoolTxPayload
	if err := json.Unmarshal(outer[1], &payload); err != nil {
		return mempoolTxParsedLine{}, false
	}

	stats := mempoolTxParsedLine{
		timestamp:       ts,
		signedActions:   len(payload.SignedActions),
		actionCounts:    make(map[string]int),
		operationCounts: make(map[string]int),
		orderCounts:     make(map[mempoolTxOrderLabel]int),
	}

	for _, signedAction := range payload.SignedActions {
		actionType := mempoolTxActionTypeLabel(signedAction.Action.Type)
		ops := mempoolTxOperationCount(signedAction.Action)
		stats.operations += ops
		stats.actionCounts[actionType]++
		stats.operationCounts[actionType] += ops

		for _, order := range signedAction.Action.Orders {
			stats.orderCounts[mempoolTxOrderMetricLabel(order)]++
		}
		if signedAction.Action.Order != nil {
			stats.orderCounts[mempoolTxOrderMetricLabel(*signedAction.Action.Order)]++
		}
		for _, modify := range signedAction.Action.Modifies {
			if modify.Order != nil {
				stats.orderCounts[mempoolTxOrderMetricLabel(*modify.Order)]++
			}
		}
	}

	return stats, true
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

	if !stats.timestamp.IsZero() {
		mempoolTxsLastRecord = stats.timestamp
		metrics.HLMempoolTxsLatestTime.Set(float64(stats.timestamp.Unix()))
	}
}

// mempoolTxsLastRecord backs the sample-age gauge; single-goroutine access.
var mempoolTxsLastRecord time.Time

func mempoolTxActionTypeLabel(actionType string) string {
	if actionType == "" {
		return "other"
	}
	if mempoolTxActionTypeAllowlist[actionType] {
		return actionType
	}
	return "other"
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

func mempoolTxOrderMetricLabel(order mempoolTxOrder) mempoolTxOrderLabel {
	side := "unknown"
	if order.IsBuy != nil {
		if *order.IsBuy {
			side = "buy"
		} else {
			side = "sell"
		}
	}

	tif := order.T.Limit.TIF
	if tif == "" {
		tif = "unknown"
	}
	if !mempoolTxOrderTIFAllowlist[tif] {
		tif = "other"
	}
	return mempoolTxOrderLabel{side: side, tif: tif}
}
