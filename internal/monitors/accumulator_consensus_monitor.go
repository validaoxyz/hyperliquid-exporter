package monitors

import (
	"bytes"
	"context"
	"encoding/json"
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

// StartAccumulatorConsensusMonitor watches the per-bucket accumulator files
// and accumulates their per-window deltas into Prometheus counters.
// Validator-only: silently idles on non-validator nodes.
//
// The headline signal here is rate(dropped_txs) > 0 — direct evidence the
// validator's consensus/mempool pipeline is shedding load. RoundTc and
// RoundCatchUp are the next most operator-actionable.
func StartAccumulatorConsensusMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "accumulator_buckets", "consensus")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("accumulator_consensus",
			"accumulator_buckets/consensus not present (%s); monitor idle (non-validator node)", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("accumulator_consensus", "watching %s (%d buckets)", root, len(accumulatorConsensusBuckets))

	// Initialize offsets at the current end of each existing bucket file so
	// history written before the exporter started is not replayed into the
	// counters (same no-replay rule as the streaming monitors).
	states := make(map[string]*accumulatorBucketState, len(accumulatorConsensusBuckets))
	for dirName := range accumulatorConsensusBuckets {
		st := &accumulatorBucketState{}
		if path, err := latestHourlyFile(filepath.Join(root, dirName, "hourly")); err == nil {
			if info, err := os.Stat(path); err == nil {
				st.path, st.offset = path, info.Size()
			}
		}
		states[dirName] = st
	}

	ticker := time.NewTicker(accumulatorConsensusPollInterval)
	defer ticker.Stop()

	tickAccumulator(root, states)
	metrics.MarkMonitorTick("accumulator_consensus")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickAccumulator(root, states)
			metrics.MarkMonitorTick("accumulator_consensus")
		}
	}
}

func tickAccumulator(root string, states map[string]*accumulatorBucketState) {
	for dirName, metricSuffix := range accumulatorConsensusBuckets {
		sum := drainAccumulatorBucket(filepath.Join(root, dirName, "hourly"), states[dirName])
		if sum > 0 {
			addAccumulatorDelta(metricSuffix, sum)
		}
	}
}

// drainAccumulatorBucket reads every complete line appended to the bucket
// since the last call and returns the summed deltas. When the bucket has
// rolled to a new hourly file, the tail of the previous file is drained
// first so no window is skipped or double-counted.
func drainAccumulatorBucket(hourlyRoot string, st *accumulatorBucketState) float64 {
	latest, err := latestHourlyFile(hourlyRoot)
	if err != nil {
		return 0
	}

	var sum float64
	switch {
	case st.path == "":
		// bucket file appeared after startup: everything in it is new
		st.path, st.offset = latest, 0
	case st.path != latest:
		// hour rolled over: finish the previous file, then start the new
		// one from the beginning. A pause spanning several rollovers skips
		// the intermediate files; with a 30s tick that only happens when
		// the exporter itself was down, where the no-replay rule applies.
		sum += drainAccumulatorFile(st)
		st.path, st.offset = latest, 0
	}
	sum += drainAccumulatorFile(st)
	return sum
}

// drainAccumulatorFile consumes complete lines from st.path starting at
// st.offset, advances the offset, and returns the summed deltas. A torn
// final line is left in place for the next pass.
func drainAccumulatorFile(st *accumulatorBucketState) float64 {
	data, err := os.ReadFile(st.path)
	if err != nil {
		return 0
	}
	if st.offset > int64(len(data)) {
		// file was truncated or replaced under us; start over
		st.offset = 0
	}
	chunk := data[st.offset:]
	end := bytes.LastIndexByte(chunk, '\n')
	if end < 0 {
		return 0
	}

	var sum float64
	for _, line := range bytes.Split(chunk[:end], []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var s accumulatorSample
		if err := json.Unmarshal(line, &s); err != nil || s.Time == "" {
			continue
		}
		if s.Delta > 0 {
			sum += s.Delta
		}
	}
	st.offset += int64(end + 1)
	return sum
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
