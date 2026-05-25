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
// Each leaf is JSON-lines with shape {"time","n","delta"} where n is the
// cumulative count since the source process started and delta is the
// increment in the current ~30s window. n resets on hl-node restart, so we
// model as gauge (not Prometheus counter — would look like a regression
// on every restart and break rate() queries).
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
	N     int64   `json:"n"`
	Delta float64 `json:"delta"`
}

// StartAccumulatorConsensusMonitor watches the per-bucket accumulator files
// and publishes cumulative-since-source-start gauges plus the latest delta.
// Validator-only: silently idles on non-validator nodes.
//
// The headline signal here is dropped_txs > 0 — direct evidence the
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

	ticker := time.NewTicker(accumulatorConsensusPollInterval)
	defer ticker.Stop()

	tickAccumulator(root)
	metrics.MarkMonitorTick("accumulator_consensus")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickAccumulator(root)
			metrics.MarkMonitorTick("accumulator_consensus")
		}
	}
}

func tickAccumulator(root string) {
	for dirName, metricSuffix := range accumulatorConsensusBuckets {
		dir := filepath.Join(root, dirName, "hourly")
		path, err := latestHourlyFile(dir)
		if err != nil {
			continue
		}
		s, ok := readLastAccumulatorSample(path)
		if !ok {
			continue
		}
		publishAccumulatorSample(metricSuffix, s)
	}
}

// readLastAccumulatorSample tail-reads the file and returns the last
// parseable {time,n,delta} line. Guards against torn final writes.
func readLastAccumulatorSample(path string) (accumulatorSample, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return accumulatorSample{}, false
	}
	for end := len(data); end > 0; {
		for end > 0 && data[end-1] == '\n' {
			end--
		}
		if end == 0 {
			break
		}
		start := bytes.LastIndexByte(data[:end], '\n') + 1
		line := bytes.TrimSpace(data[start:end])
		if len(line) > 0 && line[0] == '{' {
			var s accumulatorSample
			if err := json.Unmarshal(line, &s); err == nil && s.Time != "" {
				return s, true
			}
		}
		end = start - 1
	}
	return accumulatorSample{}, false
}

func publishAccumulatorSample(metricSuffix string, s accumulatorSample) {
	switch metricSuffix {
	case "committed_blocks":
		metrics.HLConsensusCommittedBlocks.Set(float64(s.N))
	case "committed_txs":
		metrics.HLConsensusCommittedTxs.Set(float64(s.N))
	case "committed_tx_bytes":
		metrics.HLConsensusCommittedTxBytes.Set(float64(s.N))
	case "dropped_txs":
		metrics.HLConsensusDroppedTxs.Set(float64(s.N))
	case "round_catchup":
		metrics.HLConsensusRoundCatchup.Set(float64(s.N))
	case "round_qc":
		metrics.HLConsensusRoundQC.Set(float64(s.N))
	case "round_tc":
		metrics.HLConsensusRoundTC.Set(float64(s.N))
	case "rpc_requests_registered":
		metrics.HLConsensusRPCRequestsRegistered.Set(float64(s.N))
	case "rpc_requests_sent":
		metrics.HLConsensusRPCRequestsSent.Set(float64(s.N))
	}
}
