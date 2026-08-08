package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// accumulatorConsensusPollInterval matches the underlying ~30s bucket cadence
// hl-node uses when writing these files.
const accumulatorConsensusPollInterval = 30 * time.Second

// accumulatorConsensusBuckets is the allowlist of sub-directories under
// $NODE_HOME/data/accumulator_buckets/consensus/ that we expose.
// Validator nodes write these; non-validators do not.
//
// Each leaf is JSON-lines written per ~30s flush window with shape
// {"time","n","delta"}: delta is the quantity accumulated in that window
// and n is the number of accumulation events in it (n == delta only for
// CommittedBlocks, where each block adds exactly 1). Neither field is
// cumulative, so we sum deltas into Prometheus counters. Buckets are
// sparse: a bucket with no activity writes no line at all (DroppedTxs is
// usually silent), which the offset-based reader handles naturally.
var accumulatorConsensusBuckets = map[string]string{
	"CommittedBlocks":       "committed_blocks",
	"CommittedTxs":          "committed_txs",
	"CommittedTxBytes":      "committed_tx_bytes",
	"DroppedTxs":            "dropped_txs",
	"RoundCatchUp":          "round_catchup",
	"RoundQc":               "round_qc",
	"RoundTc":               "round_tc",
	"RpcRequestsRegistered": "rpc_requests_registered",
	"RpcRequestsSent":       "rpc_requests_sent",
}

type accumulatorSample struct {
	Time  string  `json:"time"`
	N     int64   `json:"n"`     // accumulation events in the window; unused
	Delta float64 `json:"delta"` // quantity accumulated in the window
}

// accumulatorBucketState tracks the read position within a bucket's current
// hourly file so every delta is counted exactly once.
type accumulatorBucketState struct {
	path   string
	offset int64
}

type accumulatorMonitorState struct {
	initialized bool
	buckets     map[string]*accumulatorBucketState
	published   map[string]float64
}

type accumulatorDrainResult struct {
	sum          float64
	validRecords int
	invalid      bool
	overflow     bool
	latestTime   time.Time
}

// StartAccumulatorConsensusMonitor watches the per-bucket accumulator files
// and accumulates their per-window deltas into Prometheus counters.
// Validator-only: silently idles on non-validator nodes.
//
// The headline signal here is rate(dropped_txs) > 0 — direct evidence the
// validator's consensus/mempool pipeline is shedding load. RoundTc and
// RoundCatchUp are the next most operator-actionable.
func StartAccumulatorConsensusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "accumulator_buckets", "consensus")
	logger.InfoComponent("accumulator_consensus", "watching %s (%d buckets)", root, len(accumulatorConsensusBuckets))
	metrics.RegisterSource(metrics.SourceAccumulatorConsensus, true)
	state := newAccumulatorMonitorState()

	ticker := time.NewTicker(accumulatorConsensusPollInterval)
	defer ticker.Stop()

	if err := tickAccumulator(root, state); err != nil {
		logger.DebugComponent("accumulator_consensus", "initial poll: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tickAccumulator(root, state); err != nil {
				logger.DebugComponent("accumulator_consensus", "poll: %v", err)
			}
		}
	}
}

func newAccumulatorMonitorState() *accumulatorMonitorState {
	state := &accumulatorMonitorState{
		buckets:   make(map[string]*accumulatorBucketState, len(accumulatorConsensusBuckets)),
		published: make(map[string]float64, len(accumulatorConsensusBuckets)),
	}
	for dirName := range accumulatorConsensusBuckets {
		state.buckets[dirName] = &accumulatorBucketState{}
	}
	return state
}

func tickAccumulator(root string, state *accumulatorMonitorState) error {
	metrics.MarkMonitorAttempt("accumulator_consensus")
	metrics.MarkSourceAttempt(metrics.SourceAccumulatorConsensus)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			state.initialized = false
			state.buckets = newAccumulatorMonitorState().buckets
			metrics.MarkSourceAbsent(metrics.SourceAccumulatorConsensus)
			return nil
		}
		metrics.MarkSourceError(metrics.SourceAccumulatorConsensus, metrics.SourceFailureStat)
		return fmt.Errorf("stat accumulator root: %w", err)
	}
	if !state.initialized {
		if err := initializeAccumulatorOffsets(root, state); err != nil {
			metrics.MarkSourceError(metrics.SourceAccumulatorConsensus, metrics.SourceFailureRead)
			return err
		}
		state.initialized = true
		metrics.MarkSourceReadOutcome(metrics.SourceAccumulatorConsensus, true)
		return nil
	}

	var newest time.Time
	validRecords := 0
	invalid := false
	var firstErr error
	for dirName, metricSuffix := range accumulatorConsensusBuckets {
		result, err := drainAccumulatorBucketResult(filepath.Join(root, dirName, "hourly"), state.buckets[dirName])
		if result.sum > 0 && !result.overflow {
			next := state.published[metricSuffix] + result.sum
			if math.IsInf(next, 0) || math.IsNaN(next) {
				result.overflow = true
				result.invalid = true
			} else {
				addAccumulatorDelta(metricSuffix, result.sum)
				state.published[metricSuffix] = next
			}
		}
		if !result.overflow {
			validRecords += result.validRecords
			if result.latestTime.After(newest) {
				newest = result.latestTime
			}
		}
		invalid = invalid || result.invalid
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}
	if validRecords > 0 {
		metrics.MarkSourceValidObservation(metrics.SourceAccumulatorConsensus, newest)
		metrics.MarkSourcePublication(metrics.SourceAccumulatorConsensus)
		metrics.MarkMonitorValidObservation("accumulator_consensus")
		metrics.MarkMonitorPublication("accumulator_consensus")
	}
	if invalid {
		metrics.MarkSourceError(metrics.SourceAccumulatorConsensus, metrics.SourceFailureSchema)
	}
	if firstErr != nil {
		metrics.MarkSourceError(metrics.SourceAccumulatorConsensus, metrics.SourceFailureRead)
		return firstErr
	}
	return nil
}

func initializeAccumulatorOffsets(root string, state *accumulatorMonitorState) error {
	for dirName := range accumulatorConsensusBuckets {
		st := state.buckets[dirName]
		path, err := latestHourlyFile(filepath.Join(root, dirName, "hourly"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("discover %s: %w", dirName, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", dirName, err)
		}
		st.path, st.offset = path, info.Size()
	}
	return nil
}

// drainAccumulatorBucket reads every complete line appended to the bucket
// since the last call and returns the summed deltas. When the bucket has
// rolled to a new hourly file, the tail of the previous file is drained
// first so no window is skipped or double-counted.
func drainAccumulatorBucket(hourlyRoot string, st *accumulatorBucketState) float64 {
	result, _ := drainAccumulatorBucketResult(hourlyRoot, st)
	return result.sum
}

func drainAccumulatorBucketResult(hourlyRoot string, st *accumulatorBucketState) (accumulatorDrainResult, error) {
	latest, err := latestHourlyFile(hourlyRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return accumulatorDrainResult{}, nil
		}
		return accumulatorDrainResult{}, err
	}

	var result accumulatorDrainResult
	switch {
	case st.path == "":
		// bucket file appeared after startup: everything in it is new
		st.path, st.offset = latest, 0
	case st.path != latest:
		// hour rolled over: finish the previous file, then start the new
		// one from the beginning. A pause spanning several rollovers skips
		// the intermediate files; with a 30s tick that only happens when
		// the exporter itself was down, where the no-replay rule applies.
		old, err := drainAccumulatorFileResult(st)
		if err != nil {
			// The retained old inode disappeared before it could be drained.
			// Report the gap once, but adopt the successor so the bucket does
			// not retry the vanished path forever.
			st.path, st.offset = latest, 0
			current, currentErr := drainAccumulatorFileResult(st)
			if currentErr == nil {
				mergeAccumulatorResult(&result, current)
			}
			if currentErr != nil {
				return result, currentErr
			}
			return result, fmt.Errorf("drain prior accumulator file: %w", err)
		}
		mergeAccumulatorResult(&result, old)
		st.path, st.offset = latest, 0
	}
	current, err := drainAccumulatorFileResult(st)
	if err != nil {
		return result, err
	}
	mergeAccumulatorResult(&result, current)
	return result, nil
}

// drainAccumulatorFile consumes complete lines from st.path starting at
// st.offset, advances the offset, and returns the summed deltas. A torn
// final line is left in place for the next pass.
func drainAccumulatorFile(st *accumulatorBucketState) float64 {
	result, _ := drainAccumulatorFileResult(st)
	return result.sum
}

func drainAccumulatorFileResult(st *accumulatorBucketState) (accumulatorDrainResult, error) {
	var result accumulatorDrainResult
	data, err := os.ReadFile(st.path)
	if err != nil {
		return result, err
	}
	if st.offset > int64(len(data)) {
		// file was truncated or replaced under us; start over
		st.offset = 0
	}
	chunk := data[st.offset:]
	end := bytes.LastIndexByte(chunk, '\n')
	if end < 0 {
		return result, nil
	}

	for _, line := range bytes.Split(chunk[:end], []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			result.invalid = true
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			result.invalid = true
			continue
		}
		var s accumulatorSample
		if unmarshalRequiredJSON(raw["time"], &s.Time) != nil || unmarshalRequiredJSON(raw["n"], &s.N) != nil || unmarshalRequiredJSON(raw["delta"], &s.Delta) != nil ||
			s.Time == "" || s.N < 0 || math.IsNaN(s.Delta) || math.IsInf(s.Delta, 0) {
			result.invalid = true
			continue
		}
		sampleTime, ok := parseVisorTime(s.Time)
		if !ok {
			result.invalid = true
			continue
		}
		result.validRecords++
		if sampleTime.After(result.latestTime) {
			result.latestTime = sampleTime
		}
		if s.Delta > 0 {
			next := result.sum + s.Delta
			if math.IsInf(next, 0) || math.IsNaN(next) {
				result.invalid = true
				result.overflow = true
				result.sum = 0
				continue
			}
			if !result.overflow {
				result.sum = next
			}
		}
	}
	st.offset += int64(end + 1)
	return result, nil
}

func mergeAccumulatorResult(dst *accumulatorDrainResult, src accumulatorDrainResult) {
	dst.overflow = dst.overflow || src.overflow
	if !dst.overflow {
		next := dst.sum + src.sum
		if math.IsInf(next, 0) || math.IsNaN(next) {
			dst.overflow = true
			dst.invalid = true
			dst.sum = 0
		} else {
			dst.sum = next
		}
	} else {
		dst.sum = 0
	}
	dst.validRecords += src.validRecords
	dst.invalid = dst.invalid || src.invalid
	if src.latestTime.After(dst.latestTime) {
		dst.latestTime = src.latestTime
	}
}

func addAccumulatorDelta(metricSuffix string, delta float64) {
	switch metricSuffix {
	case "committed_blocks":
		metrics.HLConsensusCommittedBlocks.Add(delta)
	case "committed_txs":
		metrics.HLConsensusCommittedTxs.Add(delta)
	case "committed_tx_bytes":
		metrics.HLConsensusCommittedTxBytes.Add(delta)
	case "dropped_txs":
		metrics.HLConsensusDroppedTxs.Add(delta)
	case "round_catchup":
		metrics.HLConsensusRoundCatchup.Add(delta)
	case "round_qc":
		metrics.HLConsensusRoundQC.Add(delta)
	case "round_tc":
		metrics.HLConsensusRoundTC.Add(delta)
	case "rpc_requests_registered":
		metrics.HLConsensusRPCRequestsRegistered.Add(delta)
	case "rpc_requests_sent":
		metrics.HLConsensusRPCRequestsSent.Add(delta)
	}
}
