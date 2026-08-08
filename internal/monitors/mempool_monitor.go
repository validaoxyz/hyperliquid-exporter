package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var mempoolEventAllowlist = map[string]string{
	"add_tx":                           "add_tx",
	"verify_block":                     "verify_block",
	"committed":                        "committed",
	"dropping blocks":                  "dropping_blocks",
	"dropping txs":                     "dropping_txs",
	"handle_blocks_and_txs":            "handle_blocks_and_txs",
	"register_block unknown tx hashes": "register_block_unknown_tx_hashes",
	"Size stats":                       "size_stats",
	"Pruned rpc request throttle":      "pruned_rpc_request_throttle",
}

var mempoolSizeComponents = map[string]struct{}{
	"committed_tx_hashes": {},
	"uncommitted_txs":     {},
	"blocks":              {},
	"rpc_requests":        {},
}

var mempoolErrorKinds = map[string]string{
	"MissingValidatorSetRound":  "missing_validator_set_round",
	"TxInvalidBroadcasterNonce": "tx_invalid_broadcaster_nonce",
	"BadBlockRound":             "bad_block_round",
	"TxUnexpectedBroadcaster":   "tx_unexpected_broadcaster",
	"BlockAlreadyRegistered":    "block_already_registered",
	"AddTxDuplicate":            "add_tx_duplicate",
	"AddTxCommitted":            "add_tx_committed",
	"AddTxNotSignedAction":      "add_tx_not_signed_action",
}

const maxMempoolBatchItems = 1_000_000

type mempoolObservation struct {
	sourceTime time.Time
	eventType  string
	status     string

	errorOperation string
	errorKind      string

	pruneItems  *int64
	droppedKind string
	dropped     int

	sizeSnapshot map[string]float64
	oldestAge    *float64

	parseReason string
	complete    bool
}

func StartMempoolMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "mempool", "hourly")
	metrics.RegisterSource(metrics.SourceMempool, true)
	metrics.RegisterSource(metrics.SourceMempoolSizeStats, true)
	logger.InfoComponent("mempool", "watching %s", root)

	tailStream(ctx, tailStreamOpts{
		component:   "mempool",
		name:        "mempool stream",
		rescanEvery: 2 * time.Second,
		eofSleep:    250 * time.Millisecond,
		bufSize:     1 << 20,
		resolve: func() (string, error) {
			metrics.MarkMonitorAttempt("mempool")
			metrics.MarkSourceAttempt(metrics.SourceMempool)
			path, err := latestHourlyFile(root)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					metrics.MarkSourceAbsent(metrics.SourceMempool)
					metrics.MarkSourceAbsent(metrics.SourceMempoolSizeStats)
				} else {
					metrics.MarkSourceError(metrics.SourceMempool, metrics.SourceFailureDiscovery)
				}
			}
			return path, err
		},
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("mempool")
			metrics.MarkSourceAttempt(metrics.SourceMempool)
			obs := parseMempoolObservation([]byte(line))
			publishMempoolObservation(obs)
			if obs.parseReason != "" {
				metrics.HLMempoolParserEventsTotal.WithLabelValues(obs.parseReason).Inc()
			}
			if !obs.complete {
				metrics.MarkSourceError(metrics.SourceMempool, metrics.SourceFailureSchema)
				if obs.eventType == "size_stats" {
					metrics.MarkSourceError(metrics.SourceMempoolSizeStats, metrics.SourceFailureSchema)
				}
				return
			}
			metrics.MarkSourceValidObservation(metrics.SourceMempool, obs.sourceTime)
			metrics.MarkSourcePublication(metrics.SourceMempool)
			metrics.MarkMonitorValidObservation("mempool")
			metrics.MarkMonitorPublication("mempool")
			if obs.sizeSnapshot != nil {
				metrics.MarkSourceValidObservation(metrics.SourceMempoolSizeStats, obs.sourceTime)
				metrics.MarkSourcePublication(metrics.SourceMempoolSizeStats)
			}
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(metrics.SourceMempool, true)
		},
		onFailure: func(failure tailStreamFailure) {
			markTailSourceFailure(metrics.SourceMempool, failure)
		},
	})
}

func markTailSourceFailure(source metrics.SourceID, failure tailStreamFailure) {
	stage := metrics.SourceFailureRead
	switch failure {
	case tailStreamFailureOpen:
		stage = metrics.SourceFailureOpen
	case tailStreamFailureStat:
		stage = metrics.SourceFailureStat
	case tailStreamFailureSeek, tailStreamFailureRead:
		stage = metrics.SourceFailureRead
	case tailStreamFailureRecord:
		stage = metrics.SourceFailureSchema
	}
	metrics.MarkSourceError(source, stage)
}

func parseMempoolObservation(line []byte) mempoolObservation {
	obs := mempoolObservation{status: "not_applicable"}
	if len(line) == 0 || line[0] != '[' {
		obs.parseReason = "invalid_json"
		return obs
	}

	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil || len(outer) != 2 {
		obs.parseReason = "invalid_envelope"
		return obs
	}
	var timestamp string
	if err := json.Unmarshal(outer[0], &timestamp); err != nil {
		obs.parseReason = "invalid_timestamp"
		return obs
	}
	parsedTime, ok := parseVisorTime(timestamp)
	if !ok {
		obs.parseReason = "invalid_timestamp"
		return obs
	}
	obs.sourceTime = parsedTime

	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) == 0 {
		obs.parseReason = "invalid_payload"
		return obs
	}
	var tag string
	if err := json.Unmarshal(inner[0], &tag); err != nil || tag == "" {
		obs.parseReason = "invalid_tag"
		return obs
	}

	label, known := mempoolEventAllowlist[tag]
	if !known {
		label = "other"
		obs.parseReason = "unknown_event"
	}
	obs.eventType = label
	obs.complete = true

	switch tag {
	case "add_tx", "verify_block":
		statusIndex := 3
		errorIndex := 4
		if tag == "verify_block" {
			statusIndex = 2
			errorIndex = 3
		}
		status, reason, valid := parseMempoolStatus(inner, statusIndex)
		if !valid {
			obs.status = ""
			obs.parseReason = reason
			obs.complete = false
			return obs
		}
		obs.status = status
		if reason != "" {
			obs.parseReason = reason
		}
		if status == "err" {
			kind, valid := parseMempoolErrorKind(inner, errorIndex)
			if !valid {
				obs.parseReason = "invalid_error_wrapper"
				obs.complete = false
				return obs
			}
			obs.errorOperation = tag
			obs.errorKind = kind
		}
	case "Pruned rpc request throttle":
		items, valid := parseMempoolPrune(inner)
		if !valid {
			obs.parseReason = "invalid_prune_payload"
			obs.complete = false
			return obs
		}
		obs.pruneItems = &items
	case "dropping blocks":
		count, valid := parseMempoolArrayCount(inner, 1, false)
		if !valid {
			obs.parseReason = "invalid_drop_payload"
			obs.complete = false
			return obs
		}
		obs.droppedKind, obs.dropped = "blocks", count
	case "dropping txs":
		count, valid := parseMempoolArrayCount(inner, 1, true)
		if !valid {
			obs.parseReason = "invalid_drop_payload"
			obs.complete = false
			return obs
		}
		obs.droppedKind, obs.dropped = "transactions", count
	case "Size stats":
		snapshot, oldest, valid := parseMempoolSizeSnapshot(inner, parsedTime)
		if !valid {
			obs.parseReason = "invalid_size_snapshot"
			obs.complete = false
			return obs
		}
		obs.sizeSnapshot = snapshot
		obs.oldestAge = &oldest
	}

	return obs
}

func parseMempoolStatus(inner []json.RawMessage, index int) (status, reason string, valid bool) {
	if len(inner) <= index {
		return "", "missing_status", false
	}
	if string(inner[index]) == "null" {
		return "", "null_status", false
	}
	var raw string
	if err := json.Unmarshal(inner[index], &raw); err != nil {
		return "", "invalid_status_type", false
	}
	switch raw {
	case "ok", "err":
		return raw, "", true
	default:
		return "other", "unknown_status", true
	}
}

func parseMempoolErrorKind(inner []json.RawMessage, index int) (string, bool) {
	if len(inner) <= index || string(inner[index]) == "null" {
		return "", false
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(inner[index], &wrapper); err != nil || len(wrapper) != 1 {
		return "", false
	}
	for rawKind := range wrapper {
		if kind, ok := mempoolErrorKinds[rawKind]; ok {
			return kind, true
		}
		return "other", true
	}
	return "", false
}

func parseMempoolPrune(inner []json.RawMessage) (int64, bool) {
	if len(inner) != 4 {
		return 0, false
	}
	var rpcCount, pruned int64
	if err := json.Unmarshal(inner[1], &rpcCount); err != nil || rpcCount < 0 || rpcCount > maxMempoolBatchItems {
		return 0, false
	}
	if err := json.Unmarshal(inner[2], &pruned); err != nil || pruned < 0 || pruned > maxMempoolBatchItems {
		return 0, false
	}
	var trailing []json.RawMessage
	if err := json.Unmarshal(inner[3], &trailing); err != nil || len(trailing) != 0 {
		return 0, false
	}
	return pruned, true
}

func parseMempoolArrayCount(inner []json.RawMessage, index int, requirePairs bool) (int, bool) {
	if len(inner) <= index {
		return 0, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(inner[index], &items); err != nil || len(items) > maxMempoolBatchItems {
		return 0, false
	}
	if requirePairs {
		for _, item := range items {
			var pair []json.RawMessage
			if err := json.Unmarshal(item, &pair); err != nil || len(pair) != 2 {
				return 0, false
			}
		}
	}
	return len(items), true
}

func parseMempoolSizeSnapshot(inner []json.RawMessage, sourceTime time.Time) (map[string]float64, float64, bool) {
	if len(inner) != 3 {
		return nil, 0, false
	}
	var rawPairs []json.RawMessage
	if err := json.Unmarshal(inner[1], &rawPairs); err != nil {
		return nil, 0, false
	}
	snapshot := make(map[string]float64, len(mempoolSizeComponents))
	for _, rawPair := range rawPairs {
		var pair []json.RawMessage
		if err := json.Unmarshal(rawPair, &pair); err != nil || len(pair) != 2 {
			return nil, 0, false
		}
		var key string
		var value float64
		if err := json.Unmarshal(pair[0], &key); err != nil || json.Unmarshal(pair[1], &value) != nil {
			return nil, 0, false
		}
		if _, wanted := mempoolSizeComponents[key]; !wanted {
			continue
		}
		if _, duplicate := snapshot[key]; duplicate || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, 0, false
		}
		snapshot[key] = value
	}
	if len(snapshot) != len(mempoolSizeComponents) {
		return nil, 0, false
	}

	var current []json.RawMessage
	if err := json.Unmarshal(inner[2], &current); err != nil || len(current) > maxMempoolBatchItems {
		return nil, 0, false
	}
	uncommitted := snapshot["uncommitted_txs"]
	if uncommitted != math.Trunc(uncommitted) || int(uncommitted) != len(current) {
		return nil, 0, false
	}
	oldest := 0.0
	for _, rawPair := range current {
		var pair []json.RawMessage
		if err := json.Unmarshal(rawPair, &pair); err != nil || len(pair) != 2 {
			return nil, 0, false
		}
		// Validate the hash field's type, but never retain or export it.
		var ignored string
		if err := json.Unmarshal(pair[0], &ignored); err != nil {
			return nil, 0, false
		}
		var arrivalRaw string
		if err := json.Unmarshal(pair[1], &arrivalRaw); err != nil {
			return nil, 0, false
		}
		arrival, ok := parseVisorTime(arrivalRaw)
		if !ok || arrival.After(sourceTime) {
			return nil, 0, false
		}
		age := sourceTime.Sub(arrival).Seconds()
		if age > oldest {
			oldest = age
		}
	}
	return snapshot, oldest, true
}

func publishMempoolObservation(obs mempoolObservation) {
	if obs.eventType == "" || obs.status == "" {
		return
	}
	metrics.HLMempoolEventsTotal.WithLabelValues(obs.eventType, obs.status).Inc()
	if obs.errorOperation != "" {
		metrics.HLMempoolStructuredErrorsTotal.WithLabelValues(obs.errorOperation, obs.errorKind).Inc()
	}
	if obs.pruneItems != nil {
		metrics.HLMempoolPruneEventsTotal.Inc()
		metrics.HLMempoolPrunedItemsTotal.Add(float64(*obs.pruneItems))
	}
	if obs.droppedKind != "" {
		metrics.HLMempoolDroppedItemsTotal.WithLabelValues(obs.droppedKind).Add(float64(obs.dropped))
	}
	if obs.sizeSnapshot != nil {
		for component, value := range obs.sizeSnapshot {
			metrics.HLMempoolSize.WithLabelValues(component).Set(value)
		}
		metrics.HLMempoolOldestUncommittedAgeSeconds.WithLabelValues().Set(*obs.oldestAge)
	}
}

// readMempoolEvents remains a pure committed-record reader for focused tests
// and callers outside the long-running tail loop. Unterminated input never
// advances the returned offset.
func readMempoolEvents(path string, offset int64) (int64, int, error) {
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
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return offset, processed, err
		}
		offset += int64(len(line))
		obs := parseMempoolObservation(line)
		publishMempoolObservation(obs)
		if obs.complete {
			processed++
		}
	}
	return offset, processed, nil
}
